package manifests

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"sync"
	"text/template"

	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"
)

var kbMutex sync.Mutex

type generator struct {
	config *Opts
}

type Opts struct {
	ManagerImage string
}

func DefaultOpts() *Opts {
	return &Opts{}
}

func NewGenerator(opts *Opts) *generator {
	return &generator{config: opts}
}

func (g *generator) Generate(ctx context.Context, efs fs.FS) ([]byte, error) {
	kbMutex.Lock()
	defer kbMutex.Unlock()

	fsys := filesys.MakeFsInMemory()
	if err := fs.WalkDir(efs, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			if path == "." {
				return nil
			}

			if err := fsys.MkdirAll(path); err != nil {
				return err
			}
			return nil
		}

		// Filter anything that isn't a yaml file
		ext := filepath.Ext(d.Name())
		if !(ext == ".yaml" || ext == ".yml") {
			return nil
		}

		data, err := fs.ReadFile(efs, path)
		if err != nil {
			return err
		}

		if err := fsys.WriteFile(path, data); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return nil, err
	}

	kz, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(fsys, "default/")
	if err != nil {
		return nil, err
	}

	data, err := kz.AsYaml()
	if err != nil {
		return nil, err
	}

	return g.overlay(ctx, data)
}

func (g *generator) overlay(ctx context.Context, data []byte) ([]byte, error) {
	fsys := filesys.MakeFsInMemory()

	if err := fsys.WriteFile("generated.yaml", data); err != nil {
		return nil, err
	}

	if err := g.renderTemplate(g.config, fsys, baseKustomize, "kustomization.yaml"); err != nil {
		return nil, err
	}

	kz, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(fsys, ".")
	if err != nil {
		return nil, err
	}

	return kz.AsYaml()
}

func (g *generator) renderTemplate(obj interface{}, kfs filesys.FileSystem, tmpl string, path string) error {
	t, err := template.New("tmpl").Parse(tmpl)
	if err != nil {
		return err
	}

	var data bytes.Buffer
	writer := bufio.NewWriter(&data)
	if err := t.Execute(writer, obj); err != nil {
		return err
	}

	if err := writer.Flush(); err != nil {
		return err
	}

	return kfs.WriteFile(path, data.Bytes())
}
