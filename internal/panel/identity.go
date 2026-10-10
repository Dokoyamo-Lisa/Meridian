package panel

import (
	"encoding/json"
	"strings"
)

// The product's name. Until 1.3 it was Meridian, its pages opened in Ice (Paper on a device set to
// light) and its logo was the umbrella; since 1.3 it is Rosélune, in the Romance look, with the rose.
// The programs, services, paths and the repository keep the old name: they are what servers and
// scripts know.
const (
	productName = "Rosélune"
	formerName  = "Meridian"
)

// adoptIdentity moves the settings a panel kept from before 1.3 (they have no logo_mark yet) to the
// new name. A panel still called Meridian takes Rosélune's name, look and rose - unless someone had
// picked its look; a panel with a name of its own keeps everything it showed: its name, the umbrella
// and the look that followed the device. It says what changed, or "" when nothing needed to.
func adoptIdentity(raw []byte, s *Settings) string {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return ""
	}
	if _, ok := keys["logo_mark"]; ok {
		return "" // written by 1.3 or later
	}
	if name := strings.TrimSpace(s.SiteTitle); name == "" || name == formerName {
		s.SiteTitle, s.LogoMark = productName, "rose"
		if s.DefaultTone == "" {
			s.DefaultTone = "romance"
		}
		return formerName + " is now called " + productName + ": this panel took its new name, the rose as its logo and, unless a look had been picked, the Romance look - change any of them in Settings › Panel"
	}
	s.LogoMark = "umbrella"
	if s.DefaultTone == "" {
		s.DefaultTone = "auto" // Ice, or Paper on a device set to light: what it showed until now
	}
	return formerName + " is now called " + productName + ". This panel keeps its name, the umbrella and its look - Rosélune's rose and the Romance look are in Settings › Panel"
}
