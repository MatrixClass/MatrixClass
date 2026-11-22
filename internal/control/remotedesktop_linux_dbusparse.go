package control

// OS-independent parsing of xdg-desktop-portal results (godbus builds on every
// platform), kept out of remotedesktop_linux.go so it is unit-tested
// everywhere. The "_dbusparse" suffix is not a GOOS tag.

import (
	"github.com/godbus/dbus/v5"
)

// rdParseStartResults extracts the granted devices, the first screen-cast
// stream and the restore token from a RemoteDesktop.Start response.
func rdParseStartResults(results map[string]dbus.Variant) rdSessionInfo {
	var info rdSessionInfo
	if v, ok := results["devices"]; ok {
		if devices, ok := v.Value().(uint32); ok {
			info.devices = devices
		}
	}
	if v, ok := results["restore_token"]; ok {
		if token, ok := v.Value().(string); ok && rdValidToken(token) {
			info.restoreToken = token
		}
	}
	if v, ok := results["streams"]; ok {
		info.stream, info.width, info.height, info.hasStream = rdParseFirstStream(v.Value())
	}
	return info
}

// rdParseFirstStream decodes the first entry of an a(ua{sv}) streams value.
func rdParseFirstStream(value any) (uint32, int32, int32, bool) {
	var streams []struct {
		NodeID uint32
		Props  map[string]dbus.Variant
	}
	if err := dbus.Store([]any{value}, &streams); err != nil || len(streams) == 0 {
		return rdParseFirstStreamLoose(value)
	}
	w, h := rdParseSize(streams[0].Props)
	return streams[0].NodeID, w, h, true
}

// rdParseFirstStreamLoose handles the []interface{} shape godbus produces when
// the value was decoded without a concrete target type.
func rdParseFirstStreamLoose(value any) (uint32, int32, int32, bool) {
	list, ok := value.([][]any)
	if !ok {
		generic, ok := value.([]any)
		if !ok || len(generic) == 0 {
			return 0, 0, 0, false
		}
		first, ok := generic[0].([]any)
		if !ok {
			return 0, 0, 0, false
		}
		list = [][]any{first}
	}
	if len(list) == 0 || len(list[0]) < 1 {
		return 0, 0, 0, false
	}
	node, ok := list[0][0].(uint32)
	if !ok {
		return 0, 0, 0, false
	}
	var w, h int32
	if len(list[0]) > 1 {
		if props, ok := list[0][1].(map[string]dbus.Variant); ok {
			w, h = rdParseSize(props)
		}
	}
	return node, w, h, true
}

func rdParseSize(props map[string]dbus.Variant) (int32, int32) {
	v, ok := props["size"]
	if !ok {
		return 0, 0
	}
	switch size := v.Value().(type) {
	case []any:
		if len(size) == 2 {
			w, okW := size[0].(int32)
			h, okH := size[1].(int32)
			if okW && okH && w > 0 && h > 0 {
				return w, h
			}
		}
	case []int32:
		if len(size) == 2 && size[0] > 0 && size[1] > 0 {
			return size[0], size[1]
		}
	}
	return 0, 0
}

// rdParseResponse decodes the body of org.freedesktop.portal.Request.Response.
func rdParseResponse(body []any) (uint32, map[string]dbus.Variant, bool) {
	if len(body) < 1 {
		return 0, nil, false
	}
	code, ok := body[0].(uint32)
	if !ok {
		return 0, nil, false
	}
	results := map[string]dbus.Variant{}
	if len(body) > 1 {
		if m, ok := body[1].(map[string]dbus.Variant); ok {
			results = m
		}
	}
	return code, results, true
}

// rdSessionHandle extracts the session handle from a CreateSession response;
// the spec declares it as a string but some portals send an object path.
func rdSessionHandle(results map[string]dbus.Variant) (dbus.ObjectPath, bool) {
	v, ok := results["session_handle"]
	if !ok {
		return "", false
	}
	switch handle := v.Value().(type) {
	case string:
		path := dbus.ObjectPath(handle)
		return path, path.IsValid()
	case dbus.ObjectPath:
		return handle, handle.IsValid()
	}
	return "", false
}
