//go:build !windows

package dependencycache

func cacheRootPathMatches(requested, canonical string) bool {
	return canonical == cacheRootAfterSystemAliases(requested)
}
