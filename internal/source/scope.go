package source

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode"
)

const minimumScopeSecretBytes = 32

type scopeAccumulator struct {
	scope    Scope
	products map[string]struct{}
}

// GroupScopes groups sessions without exposing source-private filesystem or
// session references in the browser-facing representation.
func GroupScopes(sessions []Session, secret []byte) ([]Scope, error) {
	if len(secret) < minimumScopeSecretBytes {
		return nil, errors.New("source: scope secret must be at least 32 bytes")
	}
	grouped := make(map[string]*scopeAccumulator)
	for _, session := range sessions {
		namespace := string(session.Scope.Type)
		root := normalizeScopeRoot(session.Scope.Root)
		if root == "" {
			root = normalizeScopeRoot(session.Scope.Label)
		}
		if root == "" {
			root = session.Product + "\x00" + session.ID
		}
		identity := namespace + "\x00" + root
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(identity))
		key := hex.EncodeToString(mac.Sum(nil)[:12])

		group := grouped[identity]
		if group == nil {
			group = &scopeAccumulator{
				scope: Scope{
					Key:   key,
					Type:  session.Scope.Type,
					Label: safeScopeLabel(session.Scope),
				},
				products: make(map[string]struct{}),
			}
			grouped[identity] = group
		} else if label := safeScopeLabel(session.Scope); label < group.scope.Label {
			group.scope.Label = label
		}
		group.scope.Sessions = append(group.scope.Sessions, cloneSession(session))
		group.scope.SessionCount++
		if session.Product != "" {
			group.products[session.Product] = struct{}{}
		}
	}

	scopes := make([]Scope, 0, len(grouped))
	for _, group := range grouped {
		for product := range group.products {
			group.scope.Products = append(group.scope.Products, product)
		}
		sort.Strings(group.scope.Products)
		sort.Slice(group.scope.Sessions, func(i, j int) bool {
			if group.scope.Sessions[i].Product != group.scope.Sessions[j].Product {
				return group.scope.Sessions[i].Product < group.scope.Sessions[j].Product
			}
			return group.scope.Sessions[i].ID < group.scope.Sessions[j].ID
		})
		scopes = append(scopes, group.scope)
	}
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Type != scopes[j].Type {
			return scopes[i].Type < scopes[j].Type
		}
		return scopes[i].Key < scopes[j].Key
	})
	return scopes, nil
}

// Windows drive and UNC roots are normalized case-insensitively, matching the
// default semantics of Windows filesystems. POSIX paths remain case-sensitive.
func normalizeScopeRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}

	if strings.HasPrefix(root, `\\`) || strings.HasPrefix(root, "//") {
		return normalizeUNC(root)
	}
	if isAbsoluteDrivePath(root) {
		return normalizeDrive(root)
	}
	return "posix:" + path.Clean(root)
}

func isAbsoluteDrivePath(root string) bool {
	return len(root) >= 3 &&
		((root[0] >= 'a' && root[0] <= 'z') || (root[0] >= 'A' && root[0] <= 'Z')) &&
		root[1] == ':' && (root[2] == '/' || root[2] == '\\')
}

func normalizeDrive(root string) string {
	volume := strings.ToLower(root[:2])
	segments := cleanWindowsSegments(strings.Split(strings.ReplaceAll(root[3:], `\`, "/"), "/"), 0)
	if len(segments) == 0 {
		return "win-drive:" + volume + "/"
	}
	return "win-drive:" + volume + "/" + strings.Join(segments, "/")
}

func normalizeUNC(root string) string {
	root = strings.ReplaceAll(root, `\`, "/")
	parts := strings.Split(strings.TrimLeft(root, "/"), "/")
	segments := cleanWindowsSegments(parts, 2)
	return "win-unc://" + strings.Join(segments, "/")
}

func cleanWindowsSegments(parts []string, protected int) []string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(part)
		switch part {
		case "", ".":
			continue
		case "..":
			if len(cleaned) > protected {
				cleaned = cleaned[:len(cleaned)-1]
			}
		default:
			cleaned = append(cleaned, part)
		}
	}
	return cleaned
}

func safeScopeLabel(ref ScopeRef) string {
	label := strings.TrimSpace(strings.ReplaceAll(ref.Label, `\`, "/"))
	if label == "" {
		label = strings.TrimSpace(strings.ReplaceAll(ref.Root, `\`, "/"))
	}
	label = path.Base(strings.TrimSuffix(label, "/"))
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, label)
	if label == "" || label == "." || label == "/" {
		return strings.ReplaceAll(string(ref.Type), "_", " ")
	}
	return label
}
