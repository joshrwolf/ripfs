package config

import "embed"

//go:embed **/*.yaml
var EmbeddedManifests embed.FS
