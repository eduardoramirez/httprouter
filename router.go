// Forked from https://github.com/julienschmidt/httprouter
//
// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package httprouter

import (
	"context"
	"net/http"
	"strings"
)

// Param is a single URL parameter, consisting of a key and a value.
type Param struct {
	Key   string
	Value string
}

// Params is a Param-slice, as returned by the router.
// The slice is ordered, the first URL parameter is also the first slice value.
// It is therefore safe to read values by the index.
type Params []Param

// ByName returns the value of the first Param which key matches the given name.
// If no matching Param is found, an empty string is returned.
func (ps Params) ByName(name string) string {
	for _, p := range ps {
		if p.Key == name {
			return p.Value
		}
	}
	return ""
}

var catchAllParam = "$catchAllParam"

// CatchAll retrieves the remaining path matched by the catch all, if
// the catch all was not named.
func (ps Params) CatchAll() string {
	return ps.ByName(catchAllParam)
}

type paramsKey struct{}

// ParamsKey is the request context key under which URL params are stored.
var ParamsKey = paramsKey{}

// ParamsFromContext pulls the URL parameters from a request context,
// or returns nil if none are present.
func ParamsFromContext(ctx context.Context) Params {
	p, _ := ctx.Value(ParamsKey).(Params)
	return p
}

// Router is a http.Handler which can be used to dispatch requests to different
// handler functions via configurable routes
type Router struct {
	trees map[string]*node

	// If enabled, the router checks if another method is allowed for the
	// current route, if the current request can not be routed.
	// If this is the case, the request is answered with 'Method Not Allowed'
	// and HTTP status code 405.
	// If no other Method is allowed, the request is delegated to the NotFound
	// handler.
	HandleMethodNotAllowed bool

	// If enabled, the router automatically replies to OPTIONS requests.
	// Custom OPTIONS handlers take priority over automatic replies.
	HandleOPTIONS bool

	// An optional http.Handler that is called on automatic OPTIONS requests.
	// The handler is only called if HandleOPTIONS is true and no OPTIONS
	// handler for the specific path was set.
	// The "Allowed" header is set before calling the handler.
	GlobalOPTIONS http.Handler

	// Cached value of global (*) allowed methods
	globalAllowed string

	// Configurable http.Handler which is called when no matching route is
	// found. If it is not set, http.NotFound is used.
	NotFound http.Handler

	// Configurable http.Handler which is called when a request
	// cannot be routed and HandleMethodNotAllowed is true.
	// If it is not set, http.Error with http.StatusMethodNotAllowed is used.
	// The "Allow" header with allowed request methods is set before the handler
	// is called.
	MethodNotAllowed http.Handler

	// Function to handle panics recovered from http handlers.
	// It should be used to generate a error page and return the http error code
	// 500 (Internal Server Error).
	// The handler can be used to keep your server from crashing because of
	// unrecovered panics.
	PanicHandler func(http.ResponseWriter, *http.Request, interface{})

	// If enabled, the router try use URL.RawPath if one is found for route matching
	UseRawPath bool

	// If enabled, the router will match remove extra slashes from the URL before matching. It
	// not mutate the original URL.
	CleanPath bool

	// Enables automatic redirection if the current route can't be matched but a
	// handler for the path with (without) the trailing slash exists.
	// For example if /foo/ is requested but a route only exists for /foo, the
	// client is redirected to /foo with http status code 301 for GET requests
	// and 308 for all other request methods.
	RedirectTrailingSlash bool
}

// Make sure the Router conforms with the http.Handler interface
var _ http.Handler = New()

// New returns a new initialized Router.
func New() *Router {
	return &Router{
		HandleMethodNotAllowed: true,
		HandleOPTIONS:          true,
		RedirectTrailingSlash:  true,
		UseRawPath:             false,
		CleanPath:              false,
	}
}

// GET is a shortcut for router.HandlerFunc(http.MethodGet, path, handle)
func (r *Router) GET(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodGet, path, handle)
}

// HEAD is a shortcut for router.HandlerFunc(http.MethodHead, path, handle)
func (r *Router) HEAD(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodHead, path, handle)
}

// OPTIONS is a shortcut for router.HandlerFunc(http.MethodOptions, path, handle)
func (r *Router) OPTIONS(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodOptions, path, handle)
}

// POST is a shortcut for router.HandlerFunc(http.MethodPost, path, handle)
func (r *Router) POST(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodPost, path, handle)
}

// PUT is a shortcut for router.HandlerFunc(http.MethodPut, path, handle)
func (r *Router) PUT(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodPut, path, handle)
}

// PATCH is a shortcut for router.HandlerFunc(http.MethodPatch, path, handle)
func (r *Router) PATCH(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodPatch, path, handle)
}

// DELETE is a shortcut for router.HandlerFunc(http.MethodDelete, path, handle)
func (r *Router) DELETE(path string, handle func(http.ResponseWriter, *http.Request)) {
	r.HandlerFunc(http.MethodDelete, path, handle)
}

// HandlerFunc is an adapter which allows the usage of an http.HandlerFunc as a
// request handle.
func (r *Router) HandlerFunc(method, path string, handle func(http.ResponseWriter, *http.Request)) {
	if handle == nil {
		panic("handle must not be nil")
	}
	r.Handler(method, path, http.HandlerFunc(handle))
}

// Handler registers a new request handle with the given path and method.
//
// For GET, POST, PUT, PATCH and DELETE requests the respective shortcut
// functions can be used.
//
// This function is intended for bulk loading and to allow the usage of less
// frequently used, non-standardized or custom methods (e.g. for internal
// communication with a proxy).
func (r *Router) Handler(method, path string, handle http.Handler) {
	if method == "" {
		panic("method must not be empty")
	}
	if len(path) < 1 || path[0] != '/' {
		panic("path must begin with '/' in path '" + path + "'")
	}
	if handle == nil {
		panic("handle must not be nil")
	}

	if r.trees == nil {
		r.trees = make(map[string]*node)
	}

	root := r.trees[method]
	if root == nil {
		root = new(node)
		r.trees[method] = root

		r.globalAllowed = r.allowed("*", "")
	}

	root.addRoute(path, handle)
}

func (r *Router) allowed(path, reqMethod string) (allow string) {
	allowed := make([]string, 0, 9)

	if path == "*" { // server-wide
		// empty method is used for internal calls to refresh the cache
		if reqMethod == "" {
			for method := range r.trees {
				if method == http.MethodOptions {
					continue
				}
				// Add request method to list of allowed methods
				allowed = append(allowed, method)
			}
		} else {
			return r.globalAllowed
		}
	} else { // specific path
		for method := range r.trees {
			// Skip the requested method - we already tried this one
			if method == reqMethod || method == http.MethodOptions {
				continue
			}

			foundNode, _ := r.trees[method].search(path)
			if foundNode != nil && foundNode.handle != nil {
				// Add request method to list of allowed methods
				allowed = append(allowed, method)
			}
		}
	}

	if len(allowed) > 0 {
		// Add request method to list of allowed methods
		allowed = append(allowed, http.MethodOptions)

		// Sort allowed methods.
		// sort.Strings(allowed) unfortunately causes unnecessary allocations
		// due to allowed being moved to the heap and interface conversion
		for i, l := 1, len(allowed); i < l; i++ {
			for j := i; j > 0 && allowed[j] < allowed[j-1]; j-- {
				allowed[j], allowed[j-1] = allowed[j-1], allowed[j]
			}
		}

		// return as comma separated list
		return strings.Join(allowed, ", ")
	}
	return
}

func (r *Router) recv(w http.ResponseWriter, req *http.Request) {
	if rcv := recover(); rcv != nil {
		r.PanicHandler(w, req, rcv)
	}
}

func (r *Router) lookup(method, path string) (http.Handler, Params) {
	if root := r.trees[method]; root != nil {
		nodeFound, paramValues := root.search(path)
		if nodeFound == nil || nodeFound.handle == nil {
			return nil, nil
		}

		if len(paramValues) > 0 {
			params := make(Params, len(paramValues))
			for i, name := range nodeFound.wildcardNames {
				if name == "*" {
					params[i] = Param{Key: catchAllParam, Value: paramValues[i]}
				} else {
					params[i] = Param{Key: name, Value: paramValues[i]}
				}
			}
			return nodeFound.handle, params
		}

		return nodeFound.handle, nil
	}
	return nil, nil
}

// ServeHTTP makes the router implement the http.Handler interface.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.PanicHandler != nil {
		defer r.recv(w, req)
	}

	path := req.URL.Path
	if r.UseRawPath && len(req.URL.RawPath) > 0 {
		path = req.URL.RawPath
	}

	// This isn't ideal bc we're secretly matching against a different URL
	// that what we'll pass on to the handler but it solves our existing use
	// case and for the most cases it will be ok.
	if r.CleanPath {
		path = CleanPath(path)
	}

	if handle, params := r.lookup(req.Method, path); handle != nil {
		if len(params) > 0 {
			req = req.WithContext(
				context.WithValue(req.Context(), ParamsKey, params),
			)
		}
		handle.ServeHTTP(w, req)
		return
	} else if req.Method != http.MethodConnect && path != "/" {
		// Moved Permanently, request with GET method
		code := http.StatusMovedPermanently
		if req.Method != http.MethodGet {
			// Permanent Redirect, request with same method
			code = http.StatusPermanentRedirect
		}

		if r.RedirectTrailingSlash {
			var fixedPath string
			newPath := req.URL.Path
			if len(path) > 1 && path[len(path)-1] == '/' {
				fixedPath = path[:len(path)-1]
				newPath = newPath[:len(newPath)-1]
			} else {
				fixedPath = path + "/"
				newPath = newPath + "/"
			}
			if handle, _ := r.lookup(req.Method, fixedPath); handle != nil {
				req.URL.Path = newPath
				http.Redirect(w, req, req.URL.String(), code)
				return
			}
		}
	}

	if req.Method == http.MethodOptions && r.HandleOPTIONS {
		// Handle OPTIONS requests
		if allow := r.allowed(path, http.MethodOptions); allow != "" {
			w.Header().Set("Allow", allow)
			if r.GlobalOPTIONS != nil {
				r.GlobalOPTIONS.ServeHTTP(w, req)
			}
			return
		}
	} else if r.HandleMethodNotAllowed { // Handle 405
		if allow := r.allowed(path, req.Method); allow != "" {
			w.Header().Set("Allow", allow)
			if r.MethodNotAllowed != nil {
				r.MethodNotAllowed.ServeHTTP(w, req)
			} else {
				http.Error(w,
					http.StatusText(http.StatusMethodNotAllowed),
					http.StatusMethodNotAllowed,
				)
			}
			return
		}
	}

	// Handle 404
	if r.NotFound != nil {
		r.NotFound.ServeHTTP(w, req)
	} else {
		http.NotFound(w, req)
	}
}
