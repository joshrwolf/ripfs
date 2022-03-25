package offline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mholt/archiver/v4"

	"github.com/joshrwolf/ripfs/internal/consts"
)

func TestTarPayload_Bin(t *testing.T) {
	ctx := context.Background()

	tmp, err := os.MkdirTemp("", consts.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	fmt.Println(tmp)

	path := "../../../payload.tar.gz"
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	format, input, err := archiver.Identify(path, f)
	if err != nil {
		t.Fatal(err)
	}

	if ex, ok := format.(archiver.Extractor); ok {
		if err := ex.Extract(ctx, input, nil, func(ctx context.Context, f archiver.File) error {
			wp := filepath.Join(tmp, f.NameInArchive)
			if f.IsDir() {
				if err := os.MkdirAll(wp, os.ModePerm); err != nil {
					return err
				}
				return nil
			}

			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			wf, err := os.Create(wp)
			if err != nil {
				return err
			}
			defer wf.Close()

			if _, err := io.Copy(wf, rc); err != nil {
				return err
			}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	ppath := filepath.Join(tmp, "oci")
	lp, err := NewLayoutPayload(ppath)
	if err != nil {
		t.Fatal(err)
	}

	bf, err := lp.Bin()
	if err != nil {
		t.Fatal(err)
	}
	defer bf.Close()
}
