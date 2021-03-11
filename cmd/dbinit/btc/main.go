package main

import (
	"context"
	"encoding/json"
	"go-dc-wallet/app"
	"go-dc-wallet/hbtc"
	"go-dc-wallet/model"
	"go-dc-wallet/omniclient"
	"go-dc-wallet/xenv"
	"net/http"
	"time"

	"github.com/moremorefun/mcommon"
	"github.com/parnurzeal/gorequest"
)

func main() {
	xenv.EnvCreate()
	defer xenv.EnvDestroy()

	// 1. 初始化 t_app_config_int
	configIntRows := []*model.DBTAppConfigInt{
		{
			// btc 确认延迟数
			K: "btc_block_confirm_num",
			V: 2,
		},
	}
	_, err := model.SQLCreateManyTAppConfigInt(
		context.Background(),
		xenv.DbCon,
		configIntRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 2. 初始化 t_app_config_str
	// 获取可用地址
	btcAddressRows, err := app.SQLSelectTAddressKeyColByTagAndSymbol(
		context.Background(),
		xenv.DbCon,
		[]string{
			model.DBColTAddressKeyAddress,
		},
		-1,
		hbtc.CoinSymbol,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	var btcAddresses []string
	for _, btcAddressRow := range btcAddressRows {
		btcAddresses = append(btcAddresses, btcAddressRow.Address)
	}
	if len(btcAddresses) < 10 {
		btcAddresses, err = hbtc.CreateHotAddress(50)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	}

	if btcAddresses == nil {
		mcommon.Log.Errorf("btcAddresses nil")
		return
	}
	configStrRows := []*model.DBTAppConfigStr{
		{
			// btc 冷钱包地址
			K: "cold_wallet_address_btc",
			V: "",
		},
		{
			// btc 热钱包地址
			K: "hot_wallet_address_btc",
			V: btcAddresses[0],
		},
		{
			// eos 热钱包加密私钥
			K: "hot_wallet_key_eos",
			V: "",
		},
	}
	_, err = model.SQLCreateManyTAppConfigStr(
		context.Background(),
		xenv.DbCon,
		configStrRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	now := time.Now().Unix()
	// 3. 初始化 t_app_config_token_btc
	configTokenBtcRows := []*model.DBTAppConfigTokenBtc{
		{
			// Omni token 配置
			TokenIndex:      31,
			TokenSymbol:     "omni_usdt",
			ColdAddress:     "",
			HotAddress:      btcAddresses[1],
			FeeAddress:      btcAddresses[2],
			TxOrgMinBalance: "0.0",
			CreateAt:        now,
		},
	}
	_, err = model.SQLCreateManyTAppConfigTokenBtc(
		context.Background(),
		xenv.DbCon,
		configTokenBtcRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 5. 初始化 t_app_status_int
	btcRPCBlockNum, err := omniclient.RPCGetBlockCount()
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	type BtcStRespGasPrice struct {
		FastestFee  int64 `json:"fastestFee"`
		HalfHourFee int64 `json:"halfHourFee"`
		HourFee     int64 `json:"hourFee"`
	}
	gresp, body, errs := gorequest.New().
		Get("https://bitcoinfees.earn.com/api/v1/fees/recommended").
		Timeout(time.Second * 120).
		End()
	if errs != nil {
		mcommon.Log.Errorf("err: [%T] %s", errs[0], errs[0].Error())
		return
	}
	if gresp.StatusCode != http.StatusOK {
		// 状态错误
		mcommon.Log.Errorf("req status error: %d", gresp.StatusCode)
		return
	}
	var respBtc BtcStRespGasPrice
	err = json.Unmarshal([]byte(body), &respBtc)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	btcToUserGasPrice := respBtc.FastestFee
	btcToColdGasPrice := respBtc.HalfHourFee

	appStatusIntRows := []*model.DBTAppStatusInt{
		{
			// btc blocknum
			K: "btc_seek_num",
			V: btcRPCBlockNum,
		},
		{
			// omni blocknum
			K: "omni_seek_num",
			V: btcRPCBlockNum,
		},
		{
			// btc blocknum
			K: "btc_hot_fee_seek_num",
			V: btcRPCBlockNum,
		},
		{
			// btc 到冷钱包手续费
			K: "to_cold_gas_price_btc",
			V: btcToColdGasPrice,
		},
		{
			// btc 到用户手续费
			K: "to_user_gas_price_btc",
			V: btcToUserGasPrice,
		},
	}
	_, err = model.SQLCreateManyTAppStatusInt(
		context.Background(),
		xenv.DbCon,
		appStatusIntRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
}
