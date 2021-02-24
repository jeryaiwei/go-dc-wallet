package omniclient

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/moremorefun/mcommon"
	"github.com/parnurzeal/gorequest"
)

var locOmniRPCUser string
var locOmniRPCPwd string
var rpcURI string

type StRPCRespError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *StRPCRespError) Error() string {
	return fmt.Sprintf("%d %s", e.Code, e.Message)
}

type StRPCReq struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type StRPCResp struct {
	ID    string          `json:"id"`
	Error *StRPCRespError `json:"error"`
}

type StTxResultVin struct {
	Coinbase  string `json:"coinbase"`
	Txid      string `json:"txid"`
	Vout      int64  `json:"vout"`
	ScriptSig struct {
		Asm string `json:"asm"`
		Hex string `json:"hex"`
	} `json:"scriptSig"`
	TxinWitness []string `json:"txinwitness"`
	Sequence    int64    `json:"sequence"`
}

type StTxResult struct {
	Txid     string          `json:"txid"`
	Hash     string          `json:"hash"`
	Version  int64           `json:"version"`
	Size     int64           `json:"size"`
	Vsize    int64           `json:"vsize"`
	Weight   int64           `json:"weight"`
	Locktime int64           `json:"locktime"`
	Vin      []StTxResultVin `json:"vin"`
	Vout     []struct {
		Value        float64 `json:"value"`
		N            int64   `json:"n"`
		ScriptPubKey struct {
			Asm       string   `json:"asm"`
			Hex       string   `json:"hex"`
			ReqSigs   int64    `json:"reqSigs"`
			Type      string   `json:"type"`
			Addresses []string `json:"addresses"`
		} `json:"scriptPubKey,omitempty"`
	} `json:"vout"`
	Hex           string `json:"hex"`
	Blockhash     string `json:"blockhash"`
	Confirmations int64  `json:"confirmations"`
	Time          int64  `json:"time"`
	Blocktime     int64  `json:"blocktime"`
}

type StBlockResult struct {
	Hash              string        `json:"hash"`
	Confirmations     int64         `json:"confirmations"`
	Strippedsize      int64         `json:"strippedsize"`
	Size              int64         `json:"size"`
	Weight            int64         `json:"weight"`
	Height            int64         `json:"height"`
	Version           int64         `json:"version"`
	VersionHex        string        `json:"versionHex"`
	Merkleroot        string        `json:"merkleroot"`
	Tx                []*StTxResult `json:"tx"`
	Time              int64         `json:"time"`
	Mediantime        int64         `json:"mediantime"`
	Nonce             int64         `json:"nonce"`
	Bits              string        `json:"bits"`
	Difficulty        float64       `json:"difficulty"`
	Chainwork         string        `json:"chainwork"`
	NTx               int64         `json:"nTx"`
	Previousblockhash string        `json:"previousblockhash"`
	Nextblockhash     string        `json:"nextblockhash"`
}

type StOmniTx struct {
	Txid             string `json:"txid"`
	Fee              string `json:"fee"`
	Sendingaddress   string `json:"sendingaddress"`
	Referenceaddress string `json:"referenceaddress"`
	Ismine           bool   `json:"ismine"`
	Version          int64  `json:"version"`
	TypeInt          int64  `json:"type_int"`
	Type             string `json:"type"`
	Propertyid       int64  `json:"propertyid"`
	Divisible        bool   `json:"divisible"`
	Amount           string `json:"amount"`
	Valid            bool   `json:"valid"`
	Blockhash        string `json:"blockhash"`
	Blocktime        int64  `json:"blocktime"`
	Positioninblock  int64  `json:"positioninblock"`
	Block            int64  `json:"block"`
	Confirmations    int64  `json:"confirmations"`
}

type StOmniBalanceResult struct {
	Balance  string `json:"balance"`
	Reserved string `json:"reserved"`
	Frozen   string `json:"frozen"`
}

// InitClient 初始化客户端
func InitClient(omniRPCHost, omniRPCUser, omniRPCPwd string) {
	rpcURI = omniRPCHost
	locOmniRPCUser = omniRPCUser
	locOmniRPCPwd = omniRPCPwd
}

func doReq(method string, arqs []interface{}, resp interface{}) error {
	_, body, errs := gorequest.New().SetBasicAuth(locOmniRPCUser, locOmniRPCPwd).Timeout(time.Minute * 5).Post(rpcURI).Send(StRPCReq{
		Jsonrpc: "1.0",
		ID:      mcommon.GetUUIDStr(),
		Method:  method,
		Params:  arqs,
	}).EndBytes()
	if errs != nil {
		return errs[0]
	}
	err := json.Unmarshal(body, resp)
	if err != nil {
		return err
	}
	return nil
}

// RPCGetBlockCount 获取block number
func RPCGetBlockCount() (int64, error) {
	resp := struct {
		StRPCResp
		Result int64 `json:"result"`
	}{}
	err := doReq(
		"getblockcount",
		nil,
		&resp,
	)
	if err != nil {
		return 0, err
	}
	if resp.Error != nil {
		return 0, resp.Error
	}
	return resp.Result, nil
}

// RPCGetBlockHash 获取block hash
func RPCGetBlockHash(blockHeight int64) (string, error) {
	resp := struct {
		StRPCResp
		Result string `json:"result"`
	}{}
	err := doReq(
		"getblockhash",
		[]interface{}{blockHeight},
		&resp,
	)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", resp.Error
	}
	return resp.Result, nil
}

// RPCGetBlockVerbose 获取block 内容
func RPCGetBlockVerbose(blockHash string) (*StBlockResult, error) {
	resp := struct {
		StRPCResp
		Result *StBlockResult `json:"result"`
	}{}
	err := doReq(
		"getblock",
		[]interface{}{blockHash, 2},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCGetRawTransactionVerbose 获取tx
func RPCGetRawTransactionVerbose(txHash string) (*StTxResult, error) {
	resp := struct {
		StRPCResp
		Result *StTxResult `json:"result"`
	}{}
	err := doReq(
		"getrawtransaction",
		[]interface{}{txHash, 1},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCDecodeRawTransaction 解析tx
func RPCDecodeRawTransaction(txHex string) (*StTxResult, error) {
	resp := struct {
		StRPCResp
		Result *StTxResult `json:"result"`
	}{}
	err := doReq(
		"decoderawtransaction",
		[]interface{}{txHex},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCSendRawTransaction 发送tx
func RPCSendRawTransaction(txHex string) (*string, error) {
	resp := struct {
		StRPCResp
		Result *string `json:"result"`
	}{}
	err := doReq(
		"sendrawtransaction",
		[]interface{}{txHex},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCOmniListBlockTransactions 检测交易
func RPCOmniListBlockTransactions(blockNumber int64) ([]string, error) {
	resp := struct {
		StRPCResp
		Result []string `json:"result"`
	}{}
	err := doReq(
		"omni_listblocktransactions",
		[]interface{}{blockNumber},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCOmniGetTransaction 查询交易
func RPCOmniGetTransaction(txHash string) (*StOmniTx, error) {
	resp := struct {
		StRPCResp
		Result *StOmniTx `json:"result"`
	}{}
	err := doReq(
		"omni_gettransaction",
		[]interface{}{txHash},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// RPCOmniGetBalance 查询交易
func RPCOmniGetBalance(address string, tokenIndex int64) (*StOmniBalanceResult, error) {
	resp := struct {
		StRPCResp
		Result *StOmniBalanceResult `json:"result"`
	}{}
	err := doReq(
		"omni_getbalance",
		[]interface{}{address, tokenIndex},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}
