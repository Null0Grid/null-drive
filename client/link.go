package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// nulldrive:// links (Null franchise search contract: a search result's
// "open" field is nulldrive://open?id=<file id>).
//
// Any web page can try to launch a registered URL scheme, so everything in
// a link is untrusted input: only one action exists, the id must look
// exactly like a file id, and nothing from the link is ever passed to a
// shell or used as a path. The id is resolved by the server, for the
// account this computer syncs, into a path inside the sync folder.

const linkScheme = "nulldrive"

// maxLinkLen: real links are ~60 characters.
const maxLinkLen = 2048

var (
	linkIDRE   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	errBadLink = errors.New("not a valid Null Drive link")
)

type link struct {
	ID string // file id, lowercase hex
}

// parseLink accepts nulldrive://open?id=<32 hex> (and the equivalent
// nulldrive:open?id=, nulldrive://open/?id=). Anything else is errBadLink.
func parseLink(raw string) (link, error) {
	if len(raw) > maxLinkLen {
		return link{}, errBadLink
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, linkScheme) || u.User != nil || u.Fragment != "" {
		return link{}, errBadLink
	}
	action := u.Host
	if u.Opaque != "" { // nulldrive:open?id=…
		action = u.Opaque
	} else if u.Path != "" && u.Path != "/" {
		return link{}, errBadLink
	}
	if !strings.EqualFold(action, "open") {
		return link{}, errBadLink
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["id"]) != 1 {
		return link{}, errBadLink
	}
	id := strings.ToLower(q.Get("id"))
	if !linkIDRE.MatchString(id) {
		return link{}, errBadLink
	}
	return link{ID: id}, nil
}

// isLink reports whether a command-line argument is a nulldrive: URL.
func isLink(arg string) bool {
	return len(arg) > len(linkScheme) && strings.EqualFold(arg[:len(linkScheme)+1], linkScheme+":")
}

// linkFromArgs finds a link on the command line. The installer and the
// MSIX manifest register `NullDrive.exe --url "%1"`; a bare URL argument
// (other launchers, `open` on macOS) is accepted too.
//
// When a link is present the caller must ignore every other argument: the
// link comes from a web page, and a crafted one must not be able to smuggle
// in --forget or --quit.
func linkFromArgs(args []string) (string, bool) {
	for i, a := range args {
		if a == "--url" && i+1 < len(args) {
			return args[i+1], true
		}
		if isLink(a) {
			return a, true
		}
	}
	return "", false
}

// dangerousExts are opened by revealing the file in its folder instead of
// launching it: programs, scripts, installers, shortcuts and other types
// that run code or change the system when "opened". A link must never be
// a way to make the computer run something; showing the file leaves that
// decision to the user. (On Windows, the system's own list,
// AssocIsDangerous, is consulted too.)
var dangerousExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`
		exe com scr pif bat cmd ps1 ps1xml ps2 ps2xml psc1 psc2 psm1 psd1 vb vbs vbe js jse ws wsf wsc wsh
		msi msp mst msu msix msixbundle appx appxbundle appinstaller application appref-ms
		hta cpl msc jar lnk url scf reg inf ins isp dll sys ocx drv chm hlp gadget
		settingcontent-ms library-ms search-ms searchconnector-ms diagcab diagcfg diagpkg
		xll xla xlam iqy slk mshxml website shb shs vsw sct crt cer der pfx p12 mof job
		app command tool pkg mpkg dmg terminal workflow action osascript scpt scptd applescript
		sh bash zsh csh ksh fish run bin out desktop appimage deb rpm snap flatpak flatpakref py pyw pl rb php elf ko`) {
		dangerousExts[e] = true
	}
}

// isDangerous reports whether opening name could run code.
func isDangerous(name string) bool {
	n := strings.ToLower(strings.TrimRight(name, " ."))
	i := strings.LastIndex(n, ".")
	if i < 0 {
		return false // no extension: checked for the executable bit by the caller
	}
	ext := n[i+1:]
	return dangerousExts[ext] || systemSaysDangerous("."+ext)
}

// urlCommand is the command registered for nulldrive:// links, the same
// in registerURLScheme, the installer (nulldrive.nsi) and the MSIX
// manifest (as Parameters). "--url" marks the next argument as an
// untrusted link (see linkFromArgs).
func urlCommand(exe string) string { return fmt.Sprintf(`"%s" --url "%%1"`, exe) }

// commandExe extracts the program from a registered command line.
func commandExe(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, `"`); ok {
		if i := strings.IndexByte(rest, '"'); i > 0 {
			return rest[:i], nil
		}
		return "", errors.New("unterminated quote")
	}
	exe, _, _ := strings.Cut(cmd, " ")
	return exe, nil
}
