// Forked from https://github.com/julienschmidt/httprouter
//
// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package httprouter

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func printChildren(n *node, prefix string) {
	if n == nil {
		fmt.Println("Node is nil")
		return
	}
	childrenCount := 0
	if n.literals != nil {
		childrenCount += len(n.literals)
	}
	hasWildChild := false
	if n.wild != nil {
		hasWildChild = true
		childrenCount++
	}
	if n.catchAll != nil {
		hasWildChild = true
		childrenCount++
	}

	fmt.Printf(" %02d %s%s[%d] %v %t %d %v\r\n", n.priority, prefix, n.path, childrenCount, n.handle, hasWildChild, n.nType, n.indices)
	for l := len(n.path); l > 0; l-- {
		prefix += " "
	}
	for _, child := range n.literals {
		printChildren(child, prefix)
	}
	if n.wild != nil {
		printChildren(n.wild, prefix)
	}
	if n.catchAll != nil {
		printChildren(n.catchAll, prefix)
	}
}

// Used as a workaround since we can't compare functions or their addresses
var fakeHandlerValue string

func fakeHandler(val string) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {
		fakeHandlerValue = val
	}
}

type testRequests []struct {
	path       string
	nilHandler bool
	route      string
	wildcards  []string
	params     []string
}

func checkRequests(t *testing.T, tree *node, requests testRequests) {
	for _, request := range requests {
		n, ps := tree.search(request.path)

		if n == nil || n.handle == nil {
			if !request.nilHandler {
				t.Errorf("handle mismatch for route '%s': Expected non-nil handle", request.path)
			}
		} else if request.nilHandler {
			t.Errorf("handle mismatch for route '%s': Expected nil handle", request.path)
		} else {
			n.handle.ServeHTTP(nil, nil)
			if fakeHandlerValue != request.route {
				t.Errorf("handle mismatch for route '%s': Wrong handle (%s != %s)", request.path, fakeHandlerValue, request.route)
			}
		}

		if !reflect.DeepEqual(ps, request.params) {
			t.Errorf("Params mismatch for route '%s'", request.path)
		}
	}
}

func checkPriorities(t *testing.T, n *node) uint32 {
	var prio uint32

	for i := range n.literals {
		prio += checkPriorities(t, n.literals[i])
	}

	if n.wild != nil {
		if n.wild.handle != nil {
			prio++
		}
		if len(n.wild.literals) > 0 {
			prio += checkPriorities(t, n.wild.literals[0])
		}
	}

	if n.catchAll != nil {
		if n.catchAll.handle != nil {
			prio++
		}
		if len(n.catchAll.literals) > 0 {
			prio += checkPriorities(t, n.catchAll.literals[0])
		}
	}

	if n.handle != nil {
		prio++
	}

	if n.priority != prio {
		t.Errorf(
			"priority mismatch for node '%s': is %d, should be %d",
			n.path, n.priority, prio,
		)
	}

	return prio
}

func TestTreeAddAndGet(t *testing.T) {
	tree := &node{}

	routes := [...]string{
		"/hi",
		"/contact",
		"/co",
		"/c",
		"/a",
		"/ab",
		"/doc/",
		"/doc/go_faq.html",
		"/doc/go1.html",
		"/α",
		"/β",
	}
	for _, route := range routes {
		tree.addRoute(route, fakeHandler(route))
	}

	// printChildren(tree, "")

	checkRequests(t, tree, testRequests{
		{"/a", false, "/a", nil, nil},
		{"/", true, "", nil, nil},
		{"/hi", false, "/hi", nil, nil},
		{"/contact", false, "/contact", nil, nil},
		{"/co", false, "/co", nil, nil},
		{"/con", true, "", nil, nil},  // key mismatch
		{"/cona", true, "", nil, nil}, // key mismatch
		{"/no", true, "", nil, nil},   // no matching child
		{"/ab", false, "/ab", nil, nil},
		{"/α", false, "/α", nil, nil},
		{"/β", false, "/β", nil, nil},
	})

	checkPriorities(t, tree)
}

func TestTreeWildcard(t *testing.T) {
	tree := &node{}

	routes := [...]string{
		"/",
		"/cmd/:tool/:sub",
		"/cmd/:tool/",
		"/src/*filepath",
		"/search/",
		"/search/:query",
		"/user_:name",
		"/user_:name/about",
		"/files/:dir/*filepath",
		"/doc/",
		"/doc/go_faq.html",
		"/doc/go1.html",
		"/info/:user/public",
		"/info/:user/project/:project",
	}
	for _, route := range routes {
		tree.addRoute(route, fakeHandler(route))
	}

	// printChildren(tree, "")

	checkRequests(t, tree, testRequests{
		{"/", false, "/", nil, nil},
		{"/cmd/test/", false, "/cmd/:tool/", []string{"tool"}, []string{"test"}},
		{"/cmd/test", true, "", nil, nil},
		{"/cmd/test/3", false, "/cmd/:tool/:sub", []string{"tool", "sub"}, []string{"test", "3"}},
		{"/src/", true, "", nil, nil},
		{"/src/some/file.png", false, "/src/*filepath", []string{"filepath"}, []string{"some/file.png"}},
		{"/search/", false, "/search/", nil, nil},
		{"/search/someth!ng+in+ünìcodé", false, "/search/:query", []string{"query"}, []string{"someth!ng+in+ünìcodé"}},
		{"/search/someth!ng+in+ünìcodé/", true, "", nil, nil},
		{"/user_gopher", false, "/user_:name", []string{"name"}, []string{"gopher"}},
		{"/user_gopher/about", false, "/user_:name/about", []string{"name"}, []string{"gopher"}},
		{"/files/js/inc/framework.js", false, "/files/:dir/*filepath", []string{"dir", "filepath"}, []string{"js", "inc/framework.js"}},
		{"/info/gordon/public", false, "/info/:user/public", []string{"user"}, []string{"gordon"}},
		{"/info/gordon/project/go", false, "/info/:user/project/:project", []string{"user", "project"}, []string{"gordon", "go"}},
	})

	checkPriorities(t, tree)
}

func catchPanic(testFunc func()) (recv interface{}) {
	defer func() {
		recv = recover()
	}()

	testFunc()
	return
}

type testRoute struct {
	path     string
	conflict bool
}

func testRoutes(t *testing.T, routes []testRoute) {
	tree := &node{}

	for _, route := range routes {
		recv := catchPanic(func() {
			tree.addRoute(route.path, nil)
		})

		if route.conflict {
			if recv == nil {
				t.Errorf("no panic for conflicting route '%s'", route.path)
			}
		} else if recv != nil {
			t.Errorf("unexpected panic for route '%s': %v", route.path, recv)
		}
	}

	//printChildren(tree, "")
}

func TestTreeWildcardConflict(t *testing.T) {
	routes := []testRoute{
		{"/cmd/:tool/:box", false},
		{"/cmd/:tool/:box", true},
		{"/cmd/:tool/:set", true},
		{"/cmd/:tool/axe", false},
		{"/cmd/vet", false},
		{"/cmd/vet/dog", false},
		{"/src/*filepath", false},
		{"/src/*filepathx", true},
		{"/src/", false},
		{"/src1/", false},
		{"/src1/*filepath", false},
		{"/search/:query", false},
		{"/search/valid", false},
		{"/user_:name", false},
		{"/user_x", false},
		{"/user_:name", true},
		{"/id:id", false},
		{"/id/:id", false},
	}
	testRoutes(t, routes)
}

func TestTreeChildConflict(t *testing.T) {
	routes := []testRoute{
		{"/cmd/vet", false},
		{"/cmd/:tool/:sub", false},
		{"/cmd/:tool/:context", true},
		{"/src/AUTHORS", false},
		{"/src/*filepath", false},
		{"/user_x", false},
		{"/user_:name", false},
		{"/id/:id", false},
		{"/id:id", false},
		{"/:id", false},
		{"/*filepath", false},
	}
	testRoutes(t, routes)
}

func TestTreeDuplicatePath(t *testing.T) {
	tree := &node{}

	routes := [...]string{
		"/",
		"/doc/",
		"/src/*filepath",
		"/search/:query",
		"/user_:name",
	}
	for _, route := range routes {
		recv := catchPanic(func() {
			tree.addRoute(route, fakeHandler(route))
		})
		if recv != nil {
			t.Fatalf("panic inserting route '%s': %v", route, recv)
		}

		// Add again
		recv = catchPanic(func() {
			tree.addRoute(route, nil)
		})
		if recv == nil {
			t.Fatalf("no panic while inserting duplicate route '%s", route)
		}
	}

	//printChildren(tree, "")

	checkRequests(t, tree, testRequests{
		{"/", false, "/", nil, nil},
		{"/doc/", false, "/doc/", nil, nil},
		{"/src/some/file.png", false, "/src/*filepath", []string{"filepath"}, []string{"some/file.png"}},
		{"/search/someth!ng+in+ünìcodé", false, "/search/:query", []string{"query"}, []string{"someth!ng+in+ünìcodé"}},
		{"/user_gopher", false, "/user_:name", []string{"name"}, []string{"gopher"}},
	})
}

func TestTreeEmptyWildcardName(t *testing.T) {
	tree := &node{}

	routes := [...]string{
		"/user:",
		"/user:/",
		"/cmd/:/",
		"/src/*",
	}
	for _, route := range routes {
		recv := catchPanic(func() {
			tree.addRoute(route, nil)
		})
		if recv == nil {
			t.Fatalf("no panic while inserting route with empty wildcard name '%s", route)
		}
	}
}

func TestTreeCatchAllConflict(t *testing.T) {
	routes := []testRoute{
		{"/src/*filepath/x", true},
		{"/src2/", false},
		{"/src2/*filepath/x", true},
		{"/src3/*filepath", false},
		{"/src3/*filepath/x", true},
	}
	testRoutes(t, routes)
}

func TestTreeDoubleWildcard(t *testing.T) {
	const panicMsg = "only one wildcard per path segment is allowed"

	routes := [...]string{
		"/:foo:bar",
		"/:foo:bar/",
		"/:foo*bar",
	}

	for _, route := range routes {
		tree := &node{}
		recv := catchPanic(func() {
			tree.addRoute(route, nil)
		})

		if rs, ok := recv.(string); !ok || !strings.HasPrefix(rs, panicMsg) {
			t.Fatalf(`"Expected panic "%s" for route '%s', got "%v"`, panicMsg, route, recv)
		}
	}
}

func TestTreeDenormalizePath(t *testing.T) {
	p := denormalizePath("/:/hello/world/:", []string{"bar", "foo"})
	if p != "/bar/hello/world/foo" {
		t.Fatalf("Expected %s to be %s", p, "/bar/hello/world/foo")
	}

	p = denormalizePath("/:/hello/world/*", []string{"bar", "foo"})
	if p != "/bar/hello/world/foo" {
		t.Fatalf("Expected %s to be %s", p, "/bar/hello/world/foo")
	}
}
