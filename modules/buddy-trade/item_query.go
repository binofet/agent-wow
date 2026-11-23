package buddytrade

import tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"

// DecodeItemTemplateQuery reflects one complete normal item-query response.
// Callers retain their own solicited-query, session and cache ownership checks.
// The parser grants no inventory, trade or roll action authority.
func DecodeItemTemplateQuery(data []byte) (*tradev1.ItemDetails, error) {
	return parseLookupItem(data)
}
