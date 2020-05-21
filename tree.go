// Forked from https://github.com/julienschmidt/httprouter
//
// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package httprouter

import (
	"net/http"
	"strings"
)

type nodeType uint8

const (
	static nodeType = iota // default
	root
	param
	catchAll
)

type node struct {
	path     string
	priority uint32
	nType    nodeType

	literals []*node
	indices  string
	wild     *node
	catchAll *node

	handle        http.Handler
	wildcardNames []string
}

// Increments priority of the given child and reorders if necessary
func (n *node) incrementLiteralPrio(pos int) int {
	cs := n.literals
	cs[pos].priority++
	prio := cs[pos].priority

	// Adjust position (move to front)
	newPos := pos
	for ; newPos > 0 && cs[newPos-1].priority < prio; newPos-- {
		// Swap node positions
		cs[newPos-1], cs[newPos] = cs[newPos], cs[newPos-1]
	}

	// Build new index char string
	if newPos != pos {
		n.indices = n.indices[:newPos] + // Unchanged prefix, might be empty
			n.indices[pos:pos+1] + // The index char we move
			n.indices[newPos:pos] + n.indices[pos+1:] // Rest without char at 'pos'
	}

	return newPos
}

// addRoute adds a node with the given handle to the path.
// Not concurrency-safe!
func (n *node) addRoute(path string, handle http.Handler) {
	fullpath := path
	n.priority++

	path, wildcardNames := normalizePath(path)

	// Empty tree
	if len(n.path) == 0 && len(n.indices) == 0 {
		n.nType = root
		n.insertChild(fullpath, path, handle, wildcardNames)
		return
	}

walk:
	for {
		i := longestCommonPrefix(n.path, path)

		if i < len(n.path) {
			// we need to split the node at the path inflection

			child := node{
				path:          n.path[i:],
				nType:         static,
				literals:      n.literals,
				indices:       n.indices,
				wild:          n.wild,
				catchAll:      n.catchAll,
				wildcardNames: n.wildcardNames,
				handle:        n.handle,
				priority:      n.priority - 1,
			}

			n.literals = []*node{&child}
			n.wild = nil
			n.catchAll = nil
			// []byte for proper unicode char conversion, see #65
			n.indices = string([]byte{n.path[i]})
			n.path = path[:i]
			n.handle = nil
		}

		// Move the path up
		if i < len(path) {
			path = path[i:]
		} else if i == len(path) {
			path = ""
		}

		if len(path) > 0 {
			nextChar := path[0]

			// Check if we can keep walking the tree

			// on a wildcard character, follow the wild path if one exists
			if nextChar == ':' && n.wild != nil && len(path) > 1 {
				n = n.wild
				continue walk
			}

			// keep following the path if the wild node has a subtree (by definition, a wild node can only have one child)
			if n.nType == param && len(n.literals) > 0 {
				n = n.literals[0]
				n.priority++
				continue walk
			}

			// check if a child with the next path byte exists
			for i, c := range []byte(n.indices) {
				if c == nextChar {
					i = n.incrementLiteralPrio(i)
					n = n.literals[i]
					continue walk
				}
			}

			// this is something we haven't seen before so lets try inserting it
			if nextChar != ':' && nextChar != '*' {
				// []byte for proper unicode char conversion, see #65
				n.indices += string([]byte{nextChar})
				child := &node{}
				n.literals = append(n.literals, child)
				n.incrementLiteralPrio(len(n.indices) - 1)
				n = child
			}
			n.insertChild(fullpath, path, handle, wildcardNames)
			return
		}

		// node already exists, add handle if possible
		if n.handle != nil {
			panic("a handle is already registered for path '" + fullpath + "'")
		}
		n.handle = handle
		n.wildcardNames = wildcardNames
		return
	}
}

func (n *node) insertChild(fullpath string, path string, handle http.Handler, wildcardNames []string) {
	for {
		// Find the prefix until first wildcard (: or *)
		wildcard, i := findNextWildcard(path)
		if i < 0 { // No wilcard found
			break
		}

		if wildcard == ':' { // param
			if n.wild != nil {
				existingPath := denormalizePath(fullpath, n.wildcardNames)
				panic("cannot add ambigous path '" + fullpath + "', existing path '" + existingPath + "' already exists")
			}

			if i > 0 {
				// Insert prefix before the current wildcard
				n.path = path[:i]
				path = path[i:]
			}

			n.wild = &node{
				nType: param,
				path:  ":",
			}
			n = n.wild
			n.priority++

			// If the path doesn't end with the wildcard, then there
			// will be another non-wildcard subpath starting with '/'
			if len(path) > 1 {
				path = path[1:]
				child := &node{
					priority: 1,
				}
				n.literals = []*node{child}
				n = child
				continue
			}

			// Otherwise we're done. Insert the handle in the new leaf
			n.handle = handle
			n.wildcardNames = wildcardNames
			return

		} else { // catchAll
			if i != len(path)-1 {
				panic("catch-all routes are only allowed at the end of the path in path '" + fullpath + "'")
			} else if n.catchAll != nil {
				existingPath := denormalizePath(fullpath, n.wildcardNames)
				panic("cannot add ambigous path '" + fullpath + "', existing path '" + existingPath + "' already exists")
			}

			// we created space for an intermediate segment
			if n.path == "" {
				n.path = path[:i]
			}

			n.catchAll = &node{
				path:          "*",
				nType:         catchAll,
				wildcardNames: wildcardNames,
				handle:        handle,
			}
			n = n.catchAll
			n.priority++
			return
		}
	}

	// If no wildcard was found, simply insert the path and handle
	n.path = path
	n.handle = handle
	n.wildcardNames = wildcardNames
}

// recursively looks for a node at the given path
func (n *node) search(path string) (*node, []string) {
	// base case
	if len(path) == 0 {
		return n, nil
	}

	prefix := n.path

	// try going down the literals
	if strings.HasPrefix(path, prefix) {
		path = path[len(prefix):]

		if path == "" {
			return n, nil
		}

		nextChar := path[0]
		for i, c := range []byte(n.indices) {
			if c == nextChar {
				if found, params := n.literals[i].search(path); found != nil {
					return found, params
				}
			}
		}
	}

	if len(path) > 0 && path[0] != '/' {
		// if no literal matched, does the wildcard subpath work?
		if n.wild != nil {
			var token string
			// Find param end (either '/' or path end)
			if end := strings.IndexByte(path, '/'); end > 0 {
				token = path[:end]
				path = path[end:]
			} else {
				token = path
				path = ""
			}

			if len(path) > 0 {
				if len(n.wild.literals) > 0 {
					if wFound, wParams := n.wild.literals[0].search(path); wFound != nil {
						params := []string{token}
						params = append(params, wParams...)
						return wFound, params
					}
				}
			} else if n.wild.handle != nil {
				return n.wild, []string{token}
			}
		}

		// otherwise, do we have a catchall at this point that we can fallback to?
		if n.catchAll != nil {
			return n.catchAll, []string{path}
		}
	}

	// didn't find anything
	return nil, nil
}

func min(a, b int) int {
	if a <= b {
		return a
	}
	return b
}

func longestCommonPrefix(a, b string) int {
	i := 0
	max := min(len(a), len(b))
	for i < max && a[i] == b[i] {
		i++
	}
	return i
}

// Search for the first wildcard segment in the given path.
// Returns -1 as index, if no wildcard was found.
func findNextWildcard(path string) (byte, int) {
	for i, c := range path {
		if c == '*' || c == ':' {
			return byte(c), i
		}
	}
	return 0, -1
}

func normalizePath(path string) (string, []string) {
	originalPath := path

	var wildcardNames []string
	normalizedPath := strings.Builder{}

	chars := []byte(path)
	for start := 0; start < len(chars); {
		c := chars[start]
		normalizedPath.WriteByte(c)
		if c != ':' && c != '*' {
			start++
			continue
		}

		// Make sure wildcard has a name
		tokenEnd := len(chars)
	walk:
		for end, c := range chars[start+1:] {
			switch c {
			case '/':
				tokenEnd = start + 1 + end
				break walk
			case ':', '*':
				panic("only one wildcard per path segment is allowed in path '" + originalPath + "'")
			}
		}

		if tokenEnd == start+1 {
			panic("wildcards must be named with a non-empty name in path '" + originalPath + "'")
		}

		if c == '*' {
			// NOTE (eduardo): should we support '/topics/some*restofurl' ??
			if start > 1 && chars[start-1] != '/' {
				panic("catch all may not appear within a segmemt in '" + originalPath + "'")
			}
			if tokenEnd != len(chars) {
				// * should be the last thing
				panic("catch all must be the last segment in '" + originalPath + "'")
			}
		}

		wildcardName := path[start+1 : tokenEnd]
		if wildcardNames == nil {
			wildcardNames = []string{wildcardName}
		} else {
			wildcardNames = append(wildcardNames, wildcardName)
		}

		start += tokenEnd - start
	}

	return normalizedPath.String(), wildcardNames
}

func denormalizePath(normalizedPath string, wildcardNames []string) string {
	path := strings.Builder{}

	i := 0
	for _, c := range normalizedPath {
		if c != ':' && c != '*' {
			path.WriteRune(c)
		} else {
			path.WriteString(wildcardNames[i])
			i++
		}
	}

	return path.String()
}
