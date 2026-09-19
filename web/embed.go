package web

import "embed"

// StaticFiles contains the embedded vanilla HTML/JS/CSS assets for the Web UI.
//
//go:embed static/*
var StaticFiles embed.FS
