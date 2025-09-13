//go:build !oui_runtime_update

package pg_oui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// resolveOrBuild for default builds: no network download, no generation.
// It only uses provided fs.FS, or locates files in PG_OUI_DATA_DIR/user cache/current dir.
// Returns an error if not found.
func resolveOrBuild(cfg *openCfg) (fs.FS, error) {
	if cfg.fsys != nil {
		if f, err := cfg.fsys.Open(cfg.entriesName); err == nil {
			f.Close()
			return cfg.fsys, nil
		}
	}
	dir := cfg.dir
	if dir == "" {
		if env := os.Getenv("PG_OUI_DATA_DIR"); env != "" {
			dir = env
		} else if cdir, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(cdir, "pg-oui")
		} else {
			dir = "."
		}
	}
	if exists(filepath.Join(dir, cfg.entriesName)) && exists(filepath.Join(dir, cfg.vendorsName)) && exists(filepath.Join(dir, cfg.indexName)) {
		return os.DirFS(dir), nil
	}
	return nil, fmt.Errorf("pg-oui dataset not found in %q (compile-time generation required)", dir)
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
