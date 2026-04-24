package service

import "example.com/go-import-move/internal/oldhelper"

func DisplayName(name string) string {
	return oldhelper.NormalizeName(name)
}
