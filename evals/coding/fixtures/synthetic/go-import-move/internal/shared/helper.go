package shared

func NormalizeName(name string) string {
	if name == "" {
		return "anonymous"
	}
	return name
}
