package optim

// init wires every optimizer to the extensions it handles. Lookups are by
// lower-cased extension. Keeping this central makes adding new optimizers a
// one-liner.
func init() {
	Register(optimizeJSON, "json", "jsonc", "mcmeta", "mcmetac")
	Register(optimizePNG, "png")
	Register(optimizeOGG, "ogg", "oga")
	Register(optimizeShader, "vsh", "fsh", "glsl")
	Register(optimizeLang, "lang")
	Register(optimizeProperties, "properties")
}
