package sftp

import "testing"

func TestUnmarshalFileStatPreservesUIDGIDPresence(t *testing.T) {
	rootPayload := marshalUint32(nil, 0)
	rootPayload = marshalUint32(rootPayload, 0)
	stat, rest, err := unmarshalFileStat(sshFileXferAttrUIDGID, rootPayload)
	if err != nil {
		t.Fatalf("decode root ownership: %v", err)
	}
	if len(rest) != 0 || !stat.UIDGIDKnown || stat.UID != 0 || stat.GID != 0 {
		t.Fatalf("root ownership = %#v, rest=%v; expected known zero IDs", stat, rest)
	}

	stat, rest, err = unmarshalFileStat(0, nil)
	if err != nil {
		t.Fatalf("decode missing ownership: %v", err)
	}
	if len(rest) != 0 || stat.UIDGIDKnown {
		t.Fatalf("missing ownership = %#v, rest=%v; expected unknown IDs", stat, rest)
	}
}
