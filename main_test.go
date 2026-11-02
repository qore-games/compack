package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgsOrderAndSkip(t *testing.T) {
	t.Chdir(t.TempDir()) // no ./config.yml
	// Minebox's build.py puts the input first, flags after.
	cfg, err := parseArgs([]string{"pack", "-out", "x.zip", "-skip-png", "-threads", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.inPath != "pack" || cfg.Out != "x.zip" || cfg.Threads != 3 {
		t.Errorf("got in=%q out=%q threads=%d", cfg.inPath, cfg.Out, cfg.Threads)
	}
	if p := cfg.PNG; p.Recompress || p.StripMeta || p.Lossy {
		t.Errorf("-skip-png left PNG steps on: %+v", p)
	}

	// A later flag wins over a -skip-* alias.
	cfg, err = parseArgs([]string{"-skip-png", "-png-lossy", "pack"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PNG.Lossy || cfg.PNG.Recompress {
		t.Errorf("-skip-png -png-lossy: got %+v", cfg.PNG)
	}

	if _, err := parseArgs([]string{"a", "b"}); err == nil {
		t.Error("two inputs should be an error")
	}
}

func TestParseArgsConfigThenFlags(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	yml := "out: from-yaml.zip\nthreads: 0\npng:\n  lossy_quant: false\n  quant_max: 80\nogg:\n  strip_comments: false\nzip:\n  local_names: true\n"
	path := filepath.Join(dir, "my.yml")
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseArgs([]string{"pack", "-config", path, "-png-quant-max", "70"})
	if err != nil {
		t.Fatal(err)
	}
	def := defaultConfig()
	switch {
	case cfg.Out != "from-yaml.zip":
		t.Errorf("out = %q", cfg.Out)
	case cfg.Threads != def.Threads:
		t.Errorf("threads: 0 should mean one per CPU, got %d", cfg.Threads)
	case cfg.PNG.Lossy:
		t.Error("png.lossy_quant: false not applied")
	case cfg.PNG.QuantMax != 70:
		t.Errorf("the flag should override the YAML quant_max, got %d", cfg.PNG.QuantMax)
	case !cfg.PNG.Recompress || cfg.PNG.QuantMin != def.PNG.QuantMin:
		t.Error("keys absent from the YAML must keep their defaults")
	case cfg.OGG.StripComments || !cfg.OGG.Optimize:
		t.Errorf("ogg: got %+v", cfg.OGG)
	case !cfg.Zip.LocalNames:
		t.Error("zip.local_names not applied")
	}

	// ./config.yml is picked up when no -config is given.
	if err := os.WriteFile("config.yml", []byte("out: auto.zip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err = parseArgs([]string{"pack"}); err != nil || cfg.Out != "auto.zip" {
		t.Errorf("auto-loaded config.yml: out=%q err=%v", cfg.Out, err)
	}
}
