package main

import (
	"io"
	"os"
	"path/filepath"
)

// seed 首次启动时把镜像内置的 platforms.yaml 与 IP 快照播种到数据卷。
// 数据卷已有的文件不覆盖，保证用户自定义不丢。
func seed(dataDir, seedDir string) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "ip"), 0o755); err != nil {
		return err
	}
	if dataDir == seedDir {
		return nil
	}

	// platforms.yaml
	dstYaml := filepath.Join(dataDir, "platforms.yaml")
	if !exists(dstYaml) {
		_ = copyFile(filepath.Join(seedDir, "platforms.yaml"), dstYaml)
	}

	// IP 快照：仅播种数据卷里尚不存在的文件。
	seedIP := filepath.Join(seedDir, "ip")
	entries, err := os.ReadDir(seedIP)
	if err != nil {
		return nil // 没有内置快照也无妨
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(dataDir, "ip", e.Name())
		if !exists(dst) {
			_ = copyFile(filepath.Join(seedIP, e.Name()), dst)
		}
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
