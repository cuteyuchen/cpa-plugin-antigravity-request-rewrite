package main

import (
	"archive/zip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	library := flag.String("library", "", "path to the dynamic library")
	archive := flag.String("archive", "", "output zip path")
	checksum := flag.String("checksum", "", "output sha256 file path")
	flag.Parse()

	if *library == "" || *archive == "" || *checksum == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := packageRelease(*library, *archive, *checksum); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packageRelease(library, archive, checksum string) error {
	info, err := os.Stat(library)
	if err != nil {
		return fmt.Errorf("stat library: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("library is not a regular file: %s", library)
	}

	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(checksum), 0o755); err != nil {
		return fmt.Errorf("create checksum directory: %w", err)
	}

	out, err := os.Create(archive)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}

	zw := zip.NewWriter(out)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("create zip header: %w", err)
	}
	header.Name = filepath.Base(library)
	header.Method = zip.Deflate

	entry, err := zw.CreateHeader(header)
	if err != nil {
		_ = zw.Close()
		_ = out.Close()
		return fmt.Errorf("create zip entry: %w", err)
	}

	in, err := os.Open(library)
	if err != nil {
		_ = zw.Close()
		_ = out.Close()
		return fmt.Errorf("open library: %w", err)
	}
	if _, err := io.Copy(entry, in); err != nil {
		_ = in.Close()
		_ = zw.Close()
		_ = out.Close()
		return fmt.Errorf("copy library: %w", err)
	}
	if err := in.Close(); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return fmt.Errorf("close library: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return fmt.Errorf("close zip: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}

	raw, err := os.ReadFile(archive)
	if err != nil {
		return fmt.Errorf("read archive for checksum: %w", err)
	}
	sum := sha256.Sum256(raw)
	line := fmt.Sprintf("%x  %s\n", sum, filepath.Base(archive))
	if err := os.WriteFile(checksum, []byte(line), 0o644); err != nil {
		return fmt.Errorf("write checksum: %w", err)
	}
	return nil
}
