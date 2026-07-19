package texttail

func TrimTrailingCRLF(text string) string {
	end := len(text)
	for end > 0 {
		switch text[end-1] {
		case '\r', '\n':
			end--
		default:
			return text[:end]
		}
	}
	return ""
}
