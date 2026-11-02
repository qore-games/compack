package optim

// init wires every optimizer to the extensions it handles. Lookups are by
// lower-cased extension. Keeping this central makes adding new optimizers a
// one-liner.
func init() {
	Register(Spec{Kind: "JSON", Optimize: optimizeJSON}, "json", "jsonc", "mcmeta", "mcmetac")
	Register(Spec{Kind: "PNG", Slow: true, Optimize: optimizePNG}, "png")
	Register(Spec{Kind: "OGG", Slow: true, Optimize: optimizeOGG}, "ogg", "oga")
	Register(Spec{Kind: "GLSL", Optimize: optimizeShader}, "vsh", "fsh", "glsl")
	Register(Spec{Kind: "LANG", Optimize: optimizeLang}, "lang")
	Register(Spec{Kind: "PROP", Optimize: optimizeProperties}, "properties")
}
