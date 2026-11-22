package main

// version is the release the app reports. The release build sets it from the git tag with
// -ldflags "-X main.version=<tag>"; any other build shows "dev".
var version = "dev"
