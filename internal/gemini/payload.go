package gemini

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/sjson"
)

type FileData struct {
	URL      string
	FileName string
}

type ChatMetadata struct {
	CID  string
	RID  string
	RCID string
}

// BuildGeneratePayload constructs the f.req form value sent to
// StreamGenerate.  The payload follows Google's 69-element inner
// request list format used by the current Gemini web client.
//
// The f.req form value is the URL-encoded version of:
//
//	json.dumps([None, json.dumps(inner_req_list)])
//
// where inner_req_list is a 69-element array with specific indices set.
// The double-encoding (inner list as a string inside the outer array)
// is critical — the server expects outer[1] to be a JSON string that
// it json.loads again.
func BuildGeneratePayload(prompt string, reqID int, files []FileData, meta *ChatMetadata, temporaryChat bool) string {
	imagesJSON := `[]`
	if len(files) > 0 {
		for i, f := range files {
			item := `[]`
			urlArr := `[]`
			urlArr, _ = sjson.Set(urlArr, "0", f.URL)
			urlArr, _ = sjson.Set(urlArr, "1", 1)

			item, _ = sjson.SetRaw(item, "0", urlArr)
			item, _ = sjson.Set(item, "1", f.FileName)

			imagesJSON, _ = sjson.SetRaw(imagesJSON, fmt.Sprintf("%d", i), item)
		}
	}

	// Build the inner request list as a Go string. We use sjson operations
	// to construct the JSON array, then sjson.Set(outer, "1", inner) to
	// embed it as a JSON-encoded string value inside the outer envelope.
	//
	// sjson.Set with a string argument JSON-escapes it, producing exactly
	// the double-encoding the server expects: outer = [null,"<escaped>",null,null]

	// [0] message content — mirrors Python's message_content:
	//   [prompt, 0, None, req_file_data, None, None, 0]
	// When no files are attached, req_file_data is None (not []).
	// And [6] is integer 0, not None.
	msgStruct := `[]`
	msgStruct, _ = sjson.Set(msgStruct, "0", prompt)
	msgStruct, _ = sjson.Set(msgStruct, "1", 0)
	msgStruct, _ = sjson.Set(msgStruct, "2", nil)
	if len(files) > 0 {
		msgStruct, _ = sjson.SetRaw(msgStruct, "3", imagesJSON)
	} else {
		msgStruct, _ = sjson.Set(msgStruct, "3", nil)
	}
	msgStruct, _ = sjson.Set(msgStruct, "4", nil)
	msgStruct, _ = sjson.Set(msgStruct, "5", nil)
	msgStruct, _ = sjson.Set(msgStruct, "6", 0)

	inner := `[]`
	inner, _ = sjson.SetRaw(inner, "0", msgStruct)

	// [1] language
	langArr := `[]`
	langArr, _ = sjson.Set(langArr, "0", GetLanguage())
	inner, _ = sjson.SetRaw(inner, "1", langArr)

	// [2] metadata — DEFAULT_METADATA when no prior session
	if meta != nil {
		metaArr := `[]`
		metaArr, _ = sjson.Set(metaArr, "0", meta.CID)
		metaArr, _ = sjson.Set(metaArr, "1", meta.RID)
		metaArr, _ = sjson.Set(metaArr, "2", meta.RCID)
		inner, _ = sjson.SetRaw(inner, "2", metaArr)
	} else {
		defaultMeta := `["","","",null,null,null,null,null,null,""]`
		inner, _ = sjson.SetRaw(inner, "2", defaultMeta)
	}

	// [6] = [1]
	inner, _ = sjson.SetRaw(inner, "6", `[1]`)

	// [7] streaming flag — always 1 (we always stream)
	inner, _ = sjson.Set(inner, "7", 1)

	// [10] = 1, [11] = 0
	inner, _ = sjson.Set(inner, "10", 1)
	inner, _ = sjson.Set(inner, "11", 0)

	// [17] = [[0]], [18] = 0
	inner, _ = sjson.SetRaw(inner, "17", `[[0]]`)
	inner, _ = sjson.Set(inner, "18", 0)

	// [27] = 1, [30] = [4], [41] = [1]
	inner, _ = sjson.Set(inner, "27", 1)
	inner, _ = sjson.SetRaw(inner, "30", `[4]`)
	inner, _ = sjson.SetRaw(inner, "41", `[1]`)

	// [45] temporary chat flag
	if temporaryChat {
		inner, _ = sjson.Set(inner, "45", 1)
	}

	// [53] = 0
	inner, _ = sjson.Set(inner, "53", 0)

	// [59] = UUID (uppercase, like Python's uuid.uuid4())
	inner, _ = sjson.Set(inner, "59", strings.ToUpper(uuid.New().String()))

	// [61] = [], [68] = 2
	inner, _ = sjson.SetRaw(inner, "61", `[]`)
	inner, _ = sjson.Set(inner, "68", 2)

	// Wrap in the outer envelope: [null, inner_json_string, null, null]
	// sjson.Set treats inner as a string value and JSON-escapes it,
	// producing the double-encoding the server expects.
	outer := `[null,"",null,null]`
	outer, _ = sjson.Set(outer, "1", inner)

	return outer
}

func GenerateReqID() int {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return r.Intn(100000) + 100000
}
