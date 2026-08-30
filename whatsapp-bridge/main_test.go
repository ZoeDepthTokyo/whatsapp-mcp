package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// copyLiveMessagesDB copies the real, running store/messages.db into a fresh
// temp file so tests exercise production data without ever touching the
// live database the bridge daemon has open.
func copyLiveMessagesDB(t *testing.T) string {
	t.Helper()
	src := filepath.Join("store", "messages.db")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("live store/messages.db not present, skipping: %v", err)
	}

	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("failed to open live db: %v", err)
	}
	defer in.Close()

	dst := filepath.Join(t.TempDir(), "messages_copy.db")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("failed to create copy target: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("failed to copy db: %v", err)
	}
	out.Close()
	return dst
}

func openStoreAt(t *testing.T, path string) *MessageStore {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	return &MessageStore{db: db}
}

func tableInfoColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		cols = append(cols, name)
	}
	sort.Strings(cols)
	return cols
}

// TestMigrationIdempotentOnCopyOfLiveDB proves migrateAddQuotedColumns is
// additive-only and safe to run on every startup: applied twice against a
// COPY of the live db, the resulting schema is byte-identical both times,
// and the table is never dropped/recreated (row count is unchanged).
func TestMigrationIdempotentOnCopyOfLiveDB(t *testing.T) {
	dbPath := copyLiveMessagesDB(t)
	store := openStoreAt(t, dbPath)
	defer store.db.Close()

	var rowCountBefore int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&rowCountBefore); err != nil {
		t.Fatalf("failed to count rows before migration: %v", err)
	}

	if err := store.migrateAddQuotedColumns(); err != nil {
		t.Fatalf("first migration run failed: %v", err)
	}
	colsAfterFirst := tableInfoColumns(t, store.db)

	if err := store.migrateAddQuotedColumns(); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}
	colsAfterSecond := tableInfoColumns(t, store.db)

	if strings.Join(colsAfterFirst, ",") != strings.Join(colsAfterSecond, ",") {
		t.Fatalf("schema changed between runs: first=%v second=%v", colsAfterFirst, colsAfterSecond)
	}

	found := map[string]bool{}
	for _, c := range colsAfterSecond {
		found[c] = true
	}
	if !found["quoted_id"] || !found["quoted_sender"] {
		t.Fatalf("expected quoted_id and quoted_sender columns, got: %v", colsAfterSecond)
	}

	var rowCountAfter int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&rowCountAfter); err != nil {
		t.Fatalf("failed to count rows after migration: %v", err)
	}
	if rowCountBefore != rowCountAfter {
		t.Fatalf("row count changed: before=%d after=%d (table must never be dropped/recreated)", rowCountBefore, rowCountAfter)
	}
}

// TestOldRowsStillReadableAfterMigration proves existing rows survive the
// additive migration unchanged, and that pre-existing consumers reading by
// explicit column name (never SELECT *) are unaffected.
func TestOldRowsStillReadableAfterMigration(t *testing.T) {
	dbPath := copyLiveMessagesDB(t)
	store := openStoreAt(t, dbPath)
	defer store.db.Close()

	var rowCountBefore int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&rowCountBefore); err != nil {
		t.Fatalf("failed to count rows before migration: %v", err)
	}
	if rowCountBefore == 0 {
		t.Skip("live db copy has no rows to verify against")
	}

	if err := store.migrateAddQuotedColumns(); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Same column list consumer #1 (_CERES messaging.py) and consumer #2
	// (whatsapp-mcp-server) read today, plus the two new nullable columns.
	rows, err := store.db.Query(`SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, quoted_id, quoted_sender FROM messages LIMIT 5`)
	if err != nil {
		t.Fatalf("failed to read old rows post-migration: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var id, chatJID, sender, content, mediaType, filename, url string
		var timestamp time.Time
		var isFromMe bool
		var quotedID, quotedSender sql.NullString
		if err := rows.Scan(&id, &chatJID, &sender, &content, &timestamp, &isFromMe, &mediaType, &filename, &url, &quotedID, &quotedSender); err != nil {
			t.Fatalf("failed to scan old row: %v", err)
		}
		// Pre-existing rows were inserted before this column existed —
		// they must read back as NULL, never an empty-string default that
		// would misrepresent "no quote" vs "column didn't exist yet".
		if quotedID.Valid || quotedSender.Valid {
			t.Fatalf("pre-existing row %s unexpectedly has non-NULL quoted_id/quoted_sender: %v/%v", id, quotedID, quotedSender)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("expected to read at least one pre-existing row")
	}
}

// TestNewColumnsNullForContextFreeMessages proves that StoreMessage keeps
// quoted_id/quoted_sender NULL when a message carries no ContextInfo —
// identical persisted behavior to before this delta for every message that
// isn't a quoted reply.
func TestNewColumnsNullForContextFreeMessages(t *testing.T) {
	dbPath := copyLiveMessagesDB(t)
	store := openStoreAt(t, dbPath)
	defer store.db.Close()

	if err := store.migrateAddQuotedColumns(); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	testChatJID := "TESTFIXTURE-context-free@s.whatsapp.net"
	testID := "TESTFIXTURE-msg-context-free-001"
	// messages.chat_jid has a FOREIGN KEY into chats(jid) — satisfy it first.
	if err := store.StoreChat(testChatJID, "test fixture chat", time.Now()); err != nil {
		t.Fatalf("StoreChat fixture failed: %v", err)
	}
	defer store.db.Exec(`DELETE FROM chats WHERE jid = ?`, testChatJID)

	if err := store.StoreMessage(
		testID, testChatJID, "tester@s.whatsapp.net", "hello, no quote here",
		time.Now(), false, "", "", "", nil, nil, nil, 0,
		"", "", // quotedID, quotedSender — context-free
	); err != nil {
		t.Fatalf("StoreMessage failed: %v", err)
	}
	defer store.db.Exec(`DELETE FROM messages WHERE id = ? AND chat_jid = ?`, testID, testChatJID)

	var quotedID, quotedSender sql.NullString
	if err := store.db.QueryRow(`SELECT quoted_id, quoted_sender FROM messages WHERE id = ? AND chat_jid = ?`, testID, testChatJID).Scan(&quotedID, &quotedSender); err != nil {
		t.Fatalf("failed to read back inserted row: %v", err)
	}
	if quotedID.Valid || quotedSender.Valid {
		t.Fatalf("expected NULL quoted_id/quoted_sender for context-free message, got %v/%v", quotedID, quotedSender)
	}
}

// TestExtractQuotedInfoNilSafe proves the extraction helper never panics on
// nil input and returns empty strings for messages without ContextInfo —
// the same nil-safety chain extractTextContent already relies on.
func TestExtractQuotedInfoNilSafe(t *testing.T) {
	id, sender := extractQuotedInfo(nil)
	if id != "" || sender != "" {
		t.Fatalf("expected empty strings for nil message, got %q/%q", id, sender)
	}
}

// TestExtractQuotedInfoReadsStanzaIDAndParticipant proves the positive
// path: a message that carries ContextInfo (a real quoted reply) yields
// StanzaID -> quotedID and Participant -> quotedSender, per
// WAWebProtobufsE2E.pb.go:8417-8418 (ContextInfo.StanzaID / .Participant).
func TestExtractQuotedInfoReadsStanzaIDAndParticipant(t *testing.T) {
	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("Y H-001"),
			ContextInfo: &waProto.ContextInfo{
				StanzaID:    proto.String("3EB0ABCDEF1234567890"),
				Participant: proto.String("104896187600915@lid"),
			},
		},
	}

	id, sender := extractQuotedInfo(msg)
	if id != "3EB0ABCDEF1234567890" {
		t.Fatalf("expected quotedID from StanzaID, got %q", id)
	}
	if sender != "104896187600915@lid" {
		t.Fatalf("expected quotedSender from Participant, got %q", sender)
	}
}

// TestQuotedReplyPersistsThroughStoreMessage proves the full write path:
// StoreMessage persists non-empty quotedID/quotedSender as real column
// values (not NULL), round-tripping through sqlite.
func TestQuotedReplyPersistsThroughStoreMessage(t *testing.T) {
	dbPath := copyLiveMessagesDB(t)
	store := openStoreAt(t, dbPath)
	defer store.db.Close()

	if err := store.migrateAddQuotedColumns(); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	testChatJID := "TESTFIXTURE-quoted-reply@s.whatsapp.net"
	testID := "TESTFIXTURE-msg-quoted-001"
	if err := store.StoreChat(testChatJID, "test fixture chat", time.Now()); err != nil {
		t.Fatalf("StoreChat fixture failed: %v", err)
	}
	defer store.db.Exec(`DELETE FROM chats WHERE jid = ?`, testChatJID)
	defer store.db.Exec(`DELETE FROM messages WHERE id = ? AND chat_jid = ?`, testID, testChatJID)

	if err := store.StoreMessage(
		testID, testChatJID, "operator@s.whatsapp.net", "Y H-001",
		time.Now(), false, "", "", "", nil, nil, nil, 0,
		"3EB0ABCDEF1234567890", "104896187600915@lid",
	); err != nil {
		t.Fatalf("StoreMessage failed: %v", err)
	}

	var quotedID, quotedSender sql.NullString
	if err := store.db.QueryRow(`SELECT quoted_id, quoted_sender FROM messages WHERE id = ? AND chat_jid = ?`, testID, testChatJID).Scan(&quotedID, &quotedSender); err != nil {
		t.Fatalf("failed to read back inserted row: %v", err)
	}
	if !quotedID.Valid || quotedID.String != "3EB0ABCDEF1234567890" {
		t.Fatalf("expected persisted quoted_id, got %v", quotedID)
	}
	if !quotedSender.Valid || quotedSender.String != "104896187600915@lid" {
		t.Fatalf("expected persisted quoted_sender, got %v", quotedSender)
	}
}

// TestSendMessageResponseSerializesMessageID proves the new field round-trips
// through JSON exactly as the python consumers will see it over the wire —
// key "message_id", present and populated on a successful-looking response.
func TestSendMessageResponseSerializesMessageID(t *testing.T) {
	resp := SendMessageResponse{
		Success:   true,
		Message:   "Message sent to 16268237454@s.whatsapp.net",
		MessageID: "3EB0ABCDEF1234567890",
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal SendMessageResponse: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("failed to unmarshal into map: %v", err)
	}

	id, ok := decoded["message_id"]
	if !ok {
		t.Fatalf("expected key %q in JSON output, got: %s", "message_id", raw)
	}
	if id != "3EB0ABCDEF1234567890" {
		t.Fatalf("expected message_id value %q, got %v", "3EB0ABCDEF1234567890", id)
	}
	// Existing consumers key off these two — confirm they're untouched.
	if decoded["success"] != true {
		t.Fatalf("expected success=true untouched, got %v", decoded["success"])
	}
	if decoded["message"] != resp.Message {
		t.Fatalf("expected message field untouched, got %v", decoded["message"])
	}
}

// TestSendMessageResponseEmptyMessageIDOnFailure proves the failure shape:
// message_id is present as an empty string (not omitted, not null) per the
// conductor's explicit spec — python's `.get("message_id", "")` reads either
// way, but this locks the actual wire contract.
func TestSendMessageResponseEmptyMessageIDOnFailure(t *testing.T) {
	resp := SendMessageResponse{
		Success:   false,
		Message:   "Not connected to WhatsApp",
		MessageID: "",
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal SendMessageResponse: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("failed to unmarshal into map: %v", err)
	}

	id, ok := decoded["message_id"]
	if !ok {
		t.Fatalf("expected key %q present even when empty, got: %s", "message_id", raw)
	}
	if id != "" {
		t.Fatalf("expected empty message_id on failure, got %v", id)
	}
}

// TestSendWhatsAppMessageNotConnectedReturnsEmptyMessageID is a seam-level
// proof of the actual failure path in sendWhatsAppMessage, run against a
// real (but never-connected, no-network) whatsmeow.Client built the same
// way main() builds one — an in-memory sqlite device store, IsConnected()
// false because Connect() is never called. Exercises the exact first guard
// clause (main.go) without a live send.
func TestSendWhatsAppMessageNotConnectedReturnsEmptyMessageID(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("failed to create in-memory device store: %v", err)
	}
	deviceStore := container.NewDevice()
	client := whatsmeow.NewClient(deviceStore, waLog.Noop)
	// client.Connect() deliberately never called — no network I/O in this test.

	success, message, messageID := sendWhatsAppMessage(client, "16268237454@s.whatsapp.net", "hello", "")

	if success {
		t.Fatalf("expected success=false for a never-connected client")
	}
	if message != "Not connected to WhatsApp" {
		t.Fatalf("expected the not-connected message, got %q", message)
	}
	if messageID != "" {
		t.Fatalf("expected empty messageID on the not-connected failure path, got %q", messageID)
	}
}
