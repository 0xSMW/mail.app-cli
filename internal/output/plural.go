package output

import "strconv"

// Plural formats a count with its noun, adding "es" where English needs it.
func Plural(n int, singular string) string {
	if n == 1 {
		return "1 " + singular
	}
	suffix := "s"
	for _, ending := range []string{"x", "ch", "sh", "s"} {
		if len(singular) >= len(ending) && singular[len(singular)-len(ending):] == ending {
			suffix = "es"
			break
		}
	}
	return strconv.Itoa(n) + " " + singular + suffix
}
