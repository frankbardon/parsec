package admin

// Routes returns the list of URL paths the admin UI mounts under /admin.
// The slice is kept stable so that operators (and tests) can probe each
// path without reading the file system. Order is informational — the
// router uses knownPaths internally.
func Routes() []string {
	return []string{
		"/admin",
		"/admin/",
		"/admin/index.html",
		"/admin/channels.html",
		"/admin/keys.html",
		"/admin/dlq.html",
		"/admin/limits.html",
		"/admin/login.html",
		"/admin/app.js",
		"/admin/styles.css",
	}
}

// KnownAssets returns the names of every embedded asset, in stable
// order. Used by tests to confirm the embed bundle isn't drifting.
func KnownAssets() []string {
	return []string{
		"index.html",
		"channels.html",
		"keys.html",
		"dlq.html",
		"limits.html",
		"login.html",
		"app.js",
		"styles.css",
	}
}
