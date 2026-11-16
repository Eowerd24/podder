//go:build server

package main

// Wails' server transport independently verifies its configured HTTP origins
// before dispatch. Native custom-scheme origins must not replace that policy.
const nativeWebviewTransport = false
