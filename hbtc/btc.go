package hbtc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go-dc-wallet/app"
	"go-dc-wallet/model"
	"go-dc-wallet/omniclient"
	"go-dc-wallet/xenv"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/txscript"

	"github.com/btcsuite/btcd/wire"

	"github.com/moremorefun/mcommon"

	"github.com/parnurzeal/gorequest"

	"github.com/gin-gonic/gin"

	"github.com/shopspring/decimal"
)

// btc 数据字段
const (
	CoinSymbol        = "btc"
	MinNondustOutput  = 546 // satoshis
	omniHex           = "6f6d6e69"
	omniWithReturnHex = "6a146f6d6e69"
	MaxTxSize         = 1000000
	BtcInitChange     = 210000000000
)

//var gloalGenIndex = 0

func genAddressAndAesKey() (string, string, error) {
	//defer func() {
	//	gloalGenIndex++
	//}()
	// 生成私钥
	wif, err := GetNetwork(xenv.Cfg.BtcNetworkType).CreatePrivateKey()
	if err != nil {
		return "", "", err
	}
	// 加密密钥
	wifStrEn, err := mcommon.AesEncrypt(wif.String(), xenv.Cfg.AESKey)
	if err != nil {
		return "", "", err
	}
	//// 获取地址
	//address, err := GetNetwork(xenv.Cfg.BtcNetworkType).GetAddress(wif)
	//if err != nil {
	//	return "", "", err
	//}
	//addressStr := address.EncodeAddress()
	//if gloalGenIndex%2 == 0 {
	// segwitNested
	addressScriptHash, err := GetNetwork(xenv.Cfg.BtcNetworkType).GetAddressSegwitNested(wif)
	if err != nil {
		return "", "", err
	}
	addressStr := addressScriptHash.EncodeAddress()
	//}
	return addressStr, wifStrEn, nil
}

// CreateHotAddress 创建自用地址
func CreateHotAddress(num int64) ([]string, error) {
	var rows []*model.DBTAddressKey
	var addresses []string
	// 遍历差值次数
	for i := int64(0); i < num; i++ {
		address, wifStrEn, err := genAddressAndAesKey()
		if err != nil {
			return nil, err
		}
		// 存入待添加队列
		rows = append(rows, &model.DBTAddressKey{
			Symbol:  CoinSymbol,
			Address: address,
			Pwd:     wifStrEn,
			UseTag:  -1,
		})
		addresses = append(addresses, address)
	}
	// 一次性将生成的地址存入数据库
	_, err := model.SQLCreateManyTAddressKey(
		context.Background(),
		xenv.DbCon,
		rows,
		true,
	)
	if err != nil {
		return nil, err
	}
	return addresses, nil
}

// CheckAddressFree 检测剩余地址数
func CheckAddressFree() {
	lockKey := "BtcCheckAddressFree"
	app.LockWrap(lockKey, func() {
		// 获取配置 允许的最小剩余地址数
		minFreeValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"min_free_address",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取当前剩余可用地址数
		freeCount, err := app.SQLGetTAddressKeyFreeCount(
			context.Background(),
			xenv.DbCon,
			CoinSymbol,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 如果数据库中剩余可用地址小于最小允许可用地址
		if freeCount < minFreeValue {
			var rows []*model.DBTAddressKey
			// 遍历差值次数
			for i := int64(0); i < minFreeValue-freeCount; i++ {
				address, wifStrEn, err := genAddressAndAesKey()
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 存入待添加队列
				rows = append(rows, &model.DBTAddressKey{
					Symbol:  CoinSymbol,
					Address: address,
					Pwd:     wifStrEn,
					UseTag:  0,
				})
			}
			// 一次性将生成的地址存入数据库
			_, err = model.SQLCreateManyTAddressKey(
				context.Background(),
				xenv.DbCon,
				rows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
	})
}

// CheckBlockSeek 检测到账
func CheckBlockSeek() {
	lockKey := "BtcCheckBlockSeek"
	app.LockWrap(lockKey, func() {
		// 获取配置 延迟确认数
		confirmValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"btc_block_confirm_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取状态 当前处理完成的最新的block number
		seekValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"btc_seek_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		rpcBlockNum, err := omniclient.RPCGetBlockCount()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		startI := seekValue + 1
		endI := rpcBlockNum - confirmValue + 1
		if startI < endI {
			// 获取所有token
			var tokenHotAddresses []string
			var tokenFeeAddresses []string
			tokenRows, err := app.SQLSelectTAppConfigTokenBtcColAll(
				context.Background(),
				xenv.DbCon,
				[]string{
					model.DBColTAppConfigTokenBtcID,
					model.DBColTAppConfigTokenBtcHotAddress,
					model.DBColTAppConfigTokenBtcFeeAddress,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, tokenRow := range tokenRows {
				if !mcommon.IsStringInSlice(tokenHotAddresses, tokenRow.HotAddress) {
					tokenHotAddresses = append(tokenHotAddresses, tokenRow.HotAddress)
				}
				if !mcommon.IsStringInSlice(tokenFeeAddresses, tokenRow.FeeAddress) {
					tokenFeeAddresses = append(tokenFeeAddresses, tokenRow.FeeAddress)
				}
			}
			// 遍历获取需要查询的block信息
			for i := startI; i < endI; i++ {
				//mcommon.Log.Debugf("btc check block: %d", i)
				blockHash, err := omniclient.RPCGetBlockHash(i)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 一个block
				rpcBlock, err := omniclient.RPCGetBlockVerbose(blockHash)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 目标地址
				var toAddresses []string
				type StTxWithIndex struct {
					RPCTx    *omniclient.StTxResult
					Index    int64
					IsOmniTx bool
				}
				toAddressTxMap := make(map[string][]*StTxWithIndex)
				// 来源hash
				var fromTxHashes []string
				type StVinWithIndex struct {
					TxHash string
					VoutN  int64

					SpendTxHash string
					SpendN      int64
				}
				vinMap := make(map[string]*StVinWithIndex)
				// 所有tx
				for _, rpcTx := range rpcBlock.Tx {
					for i, vin := range rpcTx.Vin {
						fromTxHash := vin.Txid
						if !mcommon.IsStringInSlice(fromTxHashes, fromTxHash) {
							fromTxHashes = append(fromTxHashes, fromTxHash)
						}
						key := fmt.Sprintf("%s-%d", vin.Txid, vin.Vout)
						vinMap[key] = &StVinWithIndex{
							TxHash:      vin.Txid,
							VoutN:       vin.Vout,
							SpendTxHash: rpcTx.Txid,
							SpendN:      int64(i),
						}
					}
					omniScript := omniWithReturnHex
					isOmniTx := false
					for _, vout := range rpcTx.Vout {
						if strings.HasPrefix(vout.ScriptPubKey.Hex, omniScript) {
							isOmniTx = true
						}
					}
					for _, vout := range rpcTx.Vout {
						if len(vout.ScriptPubKey.Addresses) == 1 {
							toAddress := vout.ScriptPubKey.Addresses[0]
							if !mcommon.IsStringInSlice(toAddresses, toAddress) {
								toAddresses = append(toAddresses, toAddress)
							}
							toAddressTxMap[toAddress] = append(toAddressTxMap[toAddress], &StTxWithIndex{
								RPCTx:    rpcTx,
								Index:    vout.N,
								IsOmniTx: isOmniTx,
							})
						}
					}
				}
				//mcommon.Log.Debugf("rpc get block: %d to addresses: %d", i, len(toAddresses))

				// 从db中查询这些地址是否是冲币地址中的地址
				dbAddressRows, err := app.SQLSelectTAddressKeyColByAddress(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTAddressKeyAddress,
						model.DBColTAddressKeyUseTag,
					},
					toAddresses,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 待插入数据
				var txBtcRows []*model.DBTTxBtc
				var txBtcUxtoRows []*model.DBTTxBtcUxto
				now := time.Now().Unix()
				// 遍历数据库地址
				for _, dbAddressRow := range dbAddressRows {
					toAddress := dbAddressRow.Address
					rpcTxWithIndexes := toAddressTxMap[toAddress]
					for _, rpcTxWithIndex := range rpcTxWithIndexes {
						rpcTx := rpcTxWithIndex.RPCTx
						voutIndex := rpcTxWithIndex.Index
						checkVout := rpcTx.Vout[voutIndex]

						voutAddress := checkVout.ScriptPubKey.Addresses[0]
						voutScript := checkVout.ScriptPubKey.Hex
						omniVinAddress := ""
						if rpcTxWithIndex.IsOmniTx {
							for _, vin := range rpcTx.Vin {
								vinAddresses, err := GetAddressesOfVin(
									GetNetwork(xenv.Cfg.BtcNetworkType).Params,
									vin,
								)
								if err != nil {
									mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
									return
								}
								if len(vinAddresses) > 0 {
									omniVinAddress = strings.Join(vinAddresses, ",")
									break
								}
							}
						}
						value := decimal.NewFromFloat(checkVout.Value).String()
						if dbAddressRow.UseTag > 0 &&
							!rpcTxWithIndex.IsOmniTx {
							// 记录数据 只记录已经获取，并且输入没有输出的记录
							txBtcRows = append(
								txBtcRows,
								&model.DBTTxBtc{
									ProductID:    dbAddressRow.UseTag,
									BlockHash:    rpcBlock.Hash,
									TxID:         rpcTx.Txid,
									VoutN:        voutIndex,
									VoutAddress:  voutAddress,
									VoutValue:    value,
									CreateTime:   now,
									HandleStatus: 0,
									HandleMsg:    "",
									HandleTime:   now,
								},
							)
						}
						uxtoType := int64(app.UxtoTypeTx)
						if dbAddressRow.UseTag < 0 {
							uxtoType = app.UxtoTypeHot
						}
						if mcommon.IsStringInSlice(tokenHotAddresses, voutAddress) {
							uxtoType = app.UxtoTypeOmniHot
						}
						if mcommon.IsStringInSlice(tokenFeeAddresses, voutAddress) {
							uxtoType = app.UxtoTypeOmniOrgFee
						}
						if rpcTxWithIndex.IsOmniTx {
							omniOutAddress := ""
							isExchanged := false
							for i := len(rpcTx.Vout) - 1; i >= 0; i-- {
								vout := rpcTx.Vout[i]
								if len(vout.ScriptPubKey.Addresses) > 0 {
									toAddress := strings.Join(vout.ScriptPubKey.Addresses, ",")
									if !isExchanged && toAddress == omniVinAddress {
										isExchanged = true
										continue
									}
									omniOutAddress = toAddress
									break
								}
							}
							if omniOutAddress == voutAddress {
								uxtoType = app.UxtoTypeOmni
							}
						}
						txBtcUxtoRows = append(
							txBtcUxtoRows,
							&model.DBTTxBtcUxto{
								UxtoType:     uxtoType,
								BlockHash:    rpcBlock.Hash,
								TxID:         rpcTx.Txid,
								VoutN:        voutIndex,
								VoutAddress:  voutAddress,
								VoutValue:    value,
								VoutScript:   voutScript,
								CreateTime:   now,
								SpendTxID:    "",
								SpendN:       0,
								HandleStatus: 0,
								HandleMsg:    "",
								HandleTime:   now,
							},
						)
					}
				}

				// 从uxto中查询txhash
				var updateUxtoRows []*model.DBTTxBtcUxto
				uxtoRows, err := app.SQLSelectTTxBtcUxtoColByTxIDs(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTTxBtcUxtoID,
						model.DBColTTxBtcUxtoTxID,
						model.DBColTTxBtcUxtoVoutN,
					},
					fromTxHashes,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				for _, uxtoRow := range uxtoRows {
					key := fmt.Sprintf("%s-%d", uxtoRow.TxID, uxtoRow.VoutN)
					rpcVin, ok := vinMap[key]
					if ok {
						updateUxtoRows = append(updateUxtoRows, &model.DBTTxBtcUxto{
							ID:           uxtoRow.ID,
							TxID:         uxtoRow.TxID,
							VoutN:        uxtoRow.VoutN,
							SpendTxID:    rpcVin.SpendTxHash,
							SpendN:       rpcVin.SpendN,
							HandleStatus: app.UxtoHandleStatusConfirm,
							HandleMsg:    "confirmed",
							HandleTime:   now,
						})
					}
				}
				// 插入数据库
				_, err = model.SQLCreateManyTTxBtc(
					context.Background(),
					xenv.DbCon,
					txBtcRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				_, err = model.SQLCreateManyTTxBtcUxto(
					context.Background(),
					xenv.DbCon,
					txBtcUxtoRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新uxto状态
				_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
					context.Background(),
					xenv.DbCon,
					updateUxtoRows,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新block num
				_, err = app.SQLUpdateTAppStatusIntByKGreater(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "btc_seek_num",
						V: i,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("SQLUpdateTAppStatusIntByK err: [%T] %s", err, err.Error())
					return
				}
			}
		}
	})
}

// CheckTxOrg 检测零钱整理
func CheckTxOrg() {
	lockKey := "BtcCheckTxOrg"
	app.LockWrap(lockKey, func() {
		// 开始事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 获取所有需要整理的uxto
		allUxtoRows, err := app.SQLSelectTTxBtcUxtoColToOrgForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxBtcUxtoID,
				model.DBColTTxBtcUxtoTxID,
				model.DBColTTxBtcUxtoVoutN,
				model.DBColTTxBtcUxtoVoutAddress,
				model.DBColTTxBtcUxtoVoutValue,
				model.DBColTTxBtcUxtoVoutScript,
			},
			app.UxtoTypeTx,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(allUxtoRows) <= 0 {
			return
		}
		// 获取冷包地址
		coldAddressValue, err := app.SQLGetTAppConfigStrValueByK(
			context.Background(),
			dbTx,
			"cold_wallet_address_btc",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取手续费配置
		feePriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			dbTx,
			"to_cold_gas_price_btc",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取私钥
		var addresses []string
		for _, uxtoRow := range allUxtoRows {
			if !mcommon.IsStringInSlice(addresses, uxtoRow.VoutAddress) {
				addresses = append(addresses, uxtoRow.VoutAddress)
			}
		}
		addressWifMap, err := GetWifMapByAddresses(
			context.Background(),
			dbTx,
			addresses,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 按5000个in拆分
		step := 5000
		for i := 0; i < len(allUxtoRows); i += step {
			endI := i + step
			if len(allUxtoRows) < endI {
				endI = len(allUxtoRows)
			}
			uxtoRows := allUxtoRows[i:endI]
			// 生成交易
			var inItems []*StBtxTxIn
			var outItems []*StBtxTxOut
			for _, uxtoRow := range uxtoRows {
				wif, ok := addressWifMap[uxtoRow.VoutAddress]
				if !ok {
					mcommon.Log.Errorf("no address key: %s", uxtoRow.VoutAddress)
					return
				}
				balance, err := decimal.NewFromString(uxtoRow.VoutValue)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				inItems = append(inItems, &StBtxTxIn{
					VinTxHash: uxtoRow.TxID,
					VinTxN:    uxtoRow.VoutN,
					VinScript: uxtoRow.VoutScript,
					Balance:   balance.Mul(decimal.NewFromInt(1e8)).IntPart(),
					Wif:       wif,
				})
			}
			tx, err := BtcMakeTx(GetNetwork(xenv.Cfg.BtcNetworkType).Params, inItems, outItems, feePriceValue, coldAddressValue)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			txSize := tx.SerializeSize()
			b := new(bytes.Buffer)
			b.Grow(txSize)
			err = tx.Serialize(b)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			//mcommon.Log.Debugf("raw tx: %s", hex.EncodeToString(b.Bytes()))
			// 准备插入数据
			now := time.Now().Unix()
			var sendRows []*model.DBTSendBtc
			var updateUxtoRows []*model.DBTTxBtcUxto
			for i, uxtoRow := range uxtoRows {
				sendHex := ""
				balanceReal := "0"
				gas := int64(0)
				gasPrice := int64(0)
				if i == 0 {
					sendHex = hex.EncodeToString(b.Bytes())
					balanceReal = (decimal.NewFromInt(tx.TxOut[0].Value).Div(decimal.NewFromInt(1e8))).String()
					gas = GetTxVsize(tx)
					gasPrice = feePriceValue
				}
				sendRows = append(sendRows, &model.DBTSendBtc{
					RelatedType:  app.SendRelationTypeUXTOOrg,
					RelatedID:    uxtoRow.ID,
					TokenID:      0,
					TxID:         tx.TxHash().String(),
					FromAddress:  uxtoRow.VoutAddress,
					ToAddress:    coldAddressValue,
					BalanceReal:  balanceReal,
					Gas:          gas,
					GasPrice:     gasPrice,
					Hex:          sendHex,
					CreateTime:   now,
					HandleStatus: 0,
					HandleMsg:    "",
					HandleTime:   now,
				})
				updateUxtoRows = append(updateUxtoRows, &model.DBTTxBtcUxto{
					ID:           uxtoRow.ID,
					SpendTxID:    tx.TxHash().String(),
					SpendN:       int64(i),
					HandleStatus: app.UxtoHandleStatusUse,
					HandleMsg:    "use",
					HandleTime:   now,
				})
			}
			// 插入数据
			_, err = model.SQLCreateManyTSendBtc(
				context.Background(),
				dbTx,
				sendRows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 更新uxto状态
			_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
				context.Background(),
				dbTx,
				updateUxtoRows,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}

		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// CheckRawTxSend 发送交易
func CheckRawTxSend() {
	lockKey := "BtcCheckRawTxSend"
	app.LockWrap(lockKey, func() {
		// 发送的数组
		var sendHexes []string

		sendRows, err := app.SQLSelectTSendBtcColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTSendBtcID,
				model.DBColTSendBtcTxID,
				model.DBColTSendBtcHex,
				model.DBColTSendBtcRelatedType,
				model.DBColTSendBtcRelatedID,
			},
			app.SendStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 首先单独处理提币，提取提币通知要使用的数据
		var withdrawIDs []int64
		for _, sendRow := range sendRows {
			switch sendRow.RelatedType {
			case app.SendRelationTypeWithdraw:
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			}
		}
		withdrawMap, err := app.SQLGetWithdrawMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawBalanceReal,
				model.DBColTWithdrawTxHash,
				model.DBColTWithdrawSymbol,
			},
			withdrawIDs,
		)
		// 产品
		var productIDs []int64
		for _, withdrawRow := range withdrawMap {
			if !mcommon.IsIntInSlice(productIDs, withdrawRow.ProductID) {
				productIDs = append(productIDs, withdrawRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		// 发送
		// 通知数据
		var notifyRows []*model.DBTProductNotify
		withdrawIDs = []int64{}
		var tokenTxIDs []int64
		now := time.Now().Unix()
		addNotifyRow := func(sendRow *model.DBTSendBtc) error {
			switch sendRow.RelatedType {
			case app.SendRelationTypeOmniOrg:
				if !mcommon.IsIntInSlice(tokenTxIDs, sendRow.RelatedID) {
					tokenTxIDs = append(tokenTxIDs, sendRow.RelatedID)
				}
			}
			// 如果是提币，创建通知信息
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				withdrawRow, ok := withdrawMap[sendRow.RelatedID]
				if !ok {
					mcommon.Log.Errorf("withdrawMap no: %d", sendRow.RelatedID)
					return nil
				}
				productRow, ok := productMap[withdrawRow.ProductID]
				if !ok {
					mcommon.Log.Errorf("productMap no: %d", withdrawRow.ProductID)
					return nil
				}
				nonce := mcommon.GetUUIDStr()
				reqObj := gin.H{
					"tx_hash":     withdrawRow.TxHash,
					"balance":     withdrawRow.BalanceReal,
					"app_name":    productRow.AppName,
					"out_serial":  withdrawRow.OutSerial,
					"address":     withdrawRow.ToAddress,
					"symbol":      withdrawRow.Symbol,
					"notify_type": app.NotifyTypeWithdrawSend,
				}
				reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
				req, err := json.Marshal(reqObj)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return err
				}
				notifyRows = append(notifyRows, &model.DBTProductNotify{
					Nonce:        nonce,
					ProductID:    withdrawRow.ProductID,
					ItemType:     app.SendRelationTypeWithdraw,
					ItemID:       withdrawRow.ID,
					NotifyType:   app.NotifyTypeWithdrawSend,
					TokenSymbol:  withdrawRow.Symbol,
					URL:          productRow.CbURL,
					Msg:          string(req),
					HandleStatus: app.NotifyStatusInit,
					HandleMsg:    "",
					CreateTime:   now,
					UpdateTime:   now,
				})
				withdrawIDs = append(withdrawIDs, withdrawRow.ID)
			}
			return nil
		}

		var sendIDs []int64
		var sendTxHashes []string
		for _, sendRow := range sendRows {
			if sendRow.Hex == "" {
				continue
			}
			_, err := omniclient.RPCSendRawTransaction(sendRow.Hex)
			if err != nil && !strings.Contains(err.Error(), "already in block chain") {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
			// 解析发送的tx,查看是否需要添加入uxto
			if !mcommon.IsStringInSlice(sendHexes, sendRow.Hex) {
				sendHexes = append(sendHexes, sendRow.Hex)
			}
			sendIDs = append(sendIDs, sendRow.ID)
			sendTxHashes = append(sendTxHashes, sendRow.TxID)
			err = addNotifyRow(sendRow)
			if err != nil {
				return
			}
		}
		for _, sendRow := range sendRows {
			if mcommon.IsStringInSlice(sendTxHashes, sendRow.TxID) {
				if !mcommon.IsIntInSlice(sendIDs, sendRow.ID) {
					sendIDs = append(sendIDs, sendRow.ID)
					err = addNotifyRow(sendRow)
					if err != nil {
						return
					}
				}
			}
		}
		// 更新提币状态
		_, err = app.SQLUpdateTWithdrawStatusByIDs(
			context.Background(),
			xenv.DbCon,
			withdrawIDs,
			&model.DBTWithdraw{
				HandleStatus: app.WithdrawStatusSend,
				HandleMsg:    "send",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 添加发送通知
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新整理状态
		_, err = app.SQLUpdateTTxBtcTokenOrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			tokenTxIDs,
			model.DBTTxBtcToken{
				OrgStatus: app.TxOrgStatusSend,
				OrgMsg:    "send",
				OrgAt:     now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 检测发送是否生成新的uxto
		err = checkSendUxto(sendHexes)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新发送状态
		_, err = app.SQLUpdateTSendBtcByIDs(
			context.Background(),
			xenv.DbCon,
			sendIDs,
			&model.DBTSendBtc{
				HandleStatus: app.SendStatusSend,
				HandleTime:   now,
				HandleMsg:    "send",
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

func checkSendUxto(hexes []string) error {
	if len(hexes) > 0 {
		// 需要添加的uxto
		var txBtcUxtoRows []*model.DBTTxBtcUxto
		now := time.Now().Unix()
		// 获取btc热钱包地址
		hotAddress, err := app.SQLGetTAppConfigStrValueByK(
			context.Background(),
			xenv.DbCon,
			"hot_wallet_address_btc",
		)
		if err != nil {
			return err
		}
		// 获取所有token
		var tokenHotAddresses []string
		var tokenFeeAddresses []string
		tokenRows, err := app.SQLSelectTAppConfigTokenBtcColAll(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTAppConfigTokenBtcID,
				model.DBColTAppConfigTokenBtcHotAddress,
				model.DBColTAppConfigTokenBtcFeeAddress,
			},
		)
		if err != nil {
			return err
		}
		for _, tokenRow := range tokenRows {
			if !mcommon.IsStringInSlice(tokenHotAddresses, tokenRow.HotAddress) {
				tokenHotAddresses = append(tokenHotAddresses, tokenRow.HotAddress)
			}
			if !mcommon.IsStringInSlice(tokenFeeAddresses, tokenRow.FeeAddress) {
				tokenFeeAddresses = append(tokenFeeAddresses, tokenRow.FeeAddress)
			}
		}
		// 遍历所有tx
		for _, txHex := range hexes {
			txBs, err := hex.DecodeString(txHex)
			if err != nil {
				return err
			}
			var msgTx wire.MsgTx
			if err := msgTx.Deserialize(bytes.NewReader(txBs)); err != nil {
				return err
			}
			isOmniTx := false
			omniIndex := int64(-1)
			omniInAddress := ""
			// 检测是否是omni交易
			for _, txOut := range msgTx.TxOut {
				if strings.HasPrefix(hex.EncodeToString(txOut.PkScript), omniWithReturnHex) {
					isOmniTx = true
					break
				}
			}
			// 检测omni交易的output
			if isOmniTx {
				for _, txIn := range msgTx.TxIn {
					vinAddresses, err := GetAddressesOfVinMsg(
						GetNetwork(xenv.Cfg.BtcNetworkType).Params,
						txIn,
					)
					if err != nil {
						return err
					}
					if len(vinAddresses) > 0 {
						omniInAddress = strings.Join(vinAddresses, ",")
						break
					}
				}
				isExchanged := false
				for i := len(msgTx.TxOut) - 1; i >= 0; i-- {
					txOut := msgTx.TxOut[i]
					_, outAdds, _, err := txscript.ExtractPkScriptAddrs(
						txOut.PkScript,
						GetNetwork(xenv.Cfg.BtcNetworkType).Params,
					)
					if err != nil {
						return err
					}
					if len(outAdds) > 0 {
						var addStrs []string
						for _, outAdd := range outAdds {
							addStrs = append(addStrs, outAdd.EncodeAddress())
						}
						if !isExchanged && strings.Join(addStrs, ",") == omniInAddress {
							isExchanged = true
							continue
						}
						omniIndex = int64(i)
						break
					}
				}
			}
			for i, txOut := range msgTx.TxOut {
				_, outAdds, _, err := txscript.ExtractPkScriptAddrs(
					txOut.PkScript,
					GetNetwork(xenv.Cfg.BtcNetworkType).Params,
				)
				if err != nil {
					return err
				}
				if len(outAdds) == 1 {
					// 地址结构正确
					toAddress := outAdds[0].EncodeAddress()
					// 输出地址
					uxtoType := -1
					if toAddress == hotAddress {
						// btc 热钱包地址
						uxtoType = app.UxtoTypeHot
					} else if mcommon.IsStringInSlice(tokenFeeAddresses, toAddress) ||
						mcommon.IsStringInSlice(tokenHotAddresses, toAddress) {
						if int64(i) == omniIndex {
							// omni 交易
							uxtoType = app.UxtoTypeOmni
						} else {
							if mcommon.IsStringInSlice(tokenFeeAddresses, toAddress) {
								// 整理手续费地址
								uxtoType = app.UxtoTypeOmniOrgFee
							} else if mcommon.IsStringInSlice(tokenHotAddresses, toAddress) {
								// 提币手续费地址
								uxtoType = app.UxtoTypeOmniHot
							}
						}
					}
					if uxtoType > 0 {
						voutScript := hex.EncodeToString(txOut.PkScript)
						value := decimal.NewFromInt(txOut.Value).Div(decimal.NewFromInt(1e8)).StringFixed(8)
						txBtcUxtoRows = append(
							txBtcUxtoRows,
							&model.DBTTxBtcUxto{
								UxtoType:     int64(uxtoType),
								BlockHash:    "",
								TxID:         msgTx.TxHash().String(),
								VoutN:        int64(i),
								VoutAddress:  toAddress,
								VoutValue:    value,
								VoutScript:   voutScript,
								CreateTime:   now,
								SpendTxID:    "",
								SpendN:       0,
								HandleStatus: 0,
								HandleMsg:    "",
								HandleTime:   now,
							},
						)
					}
				}
			}
		}
		// 创建uxto
		_, err = model.SQLCreateManyTTxBtcUxto(
			context.Background(),
			xenv.DbCon,
			txBtcUxtoRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return err
		}
	}
	return nil
}

// CheckRawTxConfirm 确认tx是否打包完成
func CheckRawTxConfirm() {
	lockKey := "BtcCheckRawTxConfirm"
	app.LockWrap(lockKey, func() {
		sendRows, err := app.SQLSelectTSendBtcColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTSendBtcID,
				model.DBColTSendBtcTxID,
				model.DBColTSendBtcHex,
				model.DBColTSendBtcRelatedType,
				model.DBColTSendBtcRelatedID,
			},
			app.SendStatusSend,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取提币信息
		var withdrawIDs []int64
		for _, sendRow := range sendRows {
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				// 提币
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			}
		}
		withdrawMap, err := app.SQLGetWithdrawMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawBalanceReal,
				model.DBColTWithdrawSymbol,
				model.DBColTWithdrawTxHash,
			},
			withdrawIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		for _, withdrawRow := range withdrawMap {
			if !mcommon.IsIntInSlice(productIDs, withdrawRow.ProductID) {
				productIDs = append(productIDs, withdrawRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		var notifyRows []*model.DBTProductNotify
		var tokenTxIDs []int64
		withdrawIDs = []int64{}
		now := time.Now().Unix()
		addWithdrawNotify := func(sendRow *model.DBTSendBtc) error {
			switch sendRow.RelatedType {
			case app.SendRelationTypeOmniOrg:
				if !mcommon.IsIntInSlice(tokenTxIDs, sendRow.RelatedID) {
					tokenTxIDs = append(tokenTxIDs, sendRow.RelatedID)
				}
			}
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				// 提币
				withdrawRow, ok := withdrawMap[sendRow.RelatedID]
				if !ok {
					mcommon.Log.Errorf("no withdrawMap: %d", sendRow.RelatedID)
					return nil
				}
				productRow, ok := productMap[withdrawRow.ProductID]
				if !ok {
					mcommon.Log.Errorf("no productMap: %d", withdrawRow.ProductID)
					return nil
				}
				nonce := mcommon.GetUUIDStr()
				reqObj := gin.H{
					"tx_hash":     withdrawRow.TxHash,
					"balance":     withdrawRow.BalanceReal,
					"app_name":    productRow.AppName,
					"out_serial":  withdrawRow.OutSerial,
					"address":     withdrawRow.ToAddress,
					"symbol":      withdrawRow.Symbol,
					"notify_type": app.NotifyTypeWithdrawConfirm,
				}
				reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
				req, err := json.Marshal(reqObj)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return err
				}
				notifyRows = append(notifyRows, &model.DBTProductNotify{
					Nonce:        nonce,
					ProductID:    withdrawRow.ProductID,
					ItemType:     app.SendRelationTypeWithdraw,
					ItemID:       withdrawRow.ID,
					NotifyType:   app.NotifyTypeWithdrawConfirm,
					TokenSymbol:  withdrawRow.Symbol,
					URL:          productRow.CbURL,
					Msg:          string(req),
					HandleStatus: app.NotifyStatusInit,
					HandleMsg:    "",
					CreateTime:   now,
					UpdateTime:   now,
				})
				withdrawIDs = append(withdrawIDs, withdrawRow.ID)
			}
			return nil
		}

		var sendIDs []int64
		var confirmHashes []string
		for _, sendRow := range sendRows {
			if !mcommon.IsStringInSlice(confirmHashes, sendRow.TxID) {
				rpcTx, err := omniclient.RPCGetRawTransactionVerbose(sendRow.TxID)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					continue
				}
				if rpcTx.Confirmations <= 0 {
					continue
				}
				confirmHashes = append(confirmHashes, sendRow.TxID)
			}
			err = addWithdrawNotify(sendRow)
			if err != nil {
				continue
			}
			// 已经确认
			sendIDs = append(sendIDs, sendRow.ID)
		}
		// 更新提币状态
		_, err = app.SQLUpdateTWithdrawStatusByIDs(
			context.Background(),
			xenv.DbCon,
			withdrawIDs,
			&model.DBTWithdraw{
				HandleStatus: app.WithdrawStatusConfirm,
				HandleMsg:    "confirmed",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 添加通知
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新整理状态
		_, err = app.SQLUpdateTTxBtcTokenOrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			tokenTxIDs,
			model.DBTTxBtcToken{
				OrgStatus: app.TxOrgStatusConfirm,
				OrgMsg:    "confirmed",
				OrgAt:     now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新发送状态
		_, err = app.SQLUpdateTSendBtcByIDs(
			context.Background(),
			xenv.DbCon,
			sendIDs,
			&model.DBTSendBtc{
				HandleStatus: app.SendStatusConfirm,
				HandleTime:   now,
				HandleMsg:    "confirmed",
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

// CheckWithdraw 检测提现
func CheckWithdraw() {
	lockKey := "BtcCheckWithdraw"
	app.LockWrap(lockKey, func() {
		// 开始事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 获取提币信息
		withdrawRows, err := app.SQLSelectTWithdrawColByStatusForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawBalanceReal,
			},
			app.WithdrawStatusInit,
			[]string{CoinSymbol},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(withdrawRows) == 0 {
			return
		}
		// 获取手续费配置
		feePriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			dbTx,
			"to_user_gas_price_btc",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取热钱包地址
		hotAddressValue, err := app.SQLGetTAppConfigStrValueByK(
			context.Background(),
			dbTx,
			"hot_wallet_address_btc",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		hotAddress := hotAddressValue
		// 获取热钱包uxto
		uxtoRows, err := app.SQLSelectTTxBtcUxtoColByAddressAndTypeForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxBtcUxtoID,
				model.DBColTTxBtcUxtoTxID,
				model.DBColTTxBtcUxtoVoutN,
				model.DBColTTxBtcUxtoVoutAddress,
				model.DBColTTxBtcUxtoVoutValue,
				model.DBColTTxBtcUxtoVoutScript,
			},
			hotAddress,
			app.UxtoTypeHot,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取私钥
		var addresses []string
		for _, uxtoRow := range uxtoRows {
			addresses = append(addresses, uxtoRow.VoutAddress)
		}
		addressWifMap, err := GetWifMapByAddresses(
			context.Background(),
			dbTx,
			addresses,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 生成交易
		// 输入金额
		inBalance := int64(0)
		// 输出金额
		outBalance := int64(0)
		// 使用到的uxto的索引
		uxtoUseIndex := 0
		// 输入信息
		var inUxtoRows []*model.DBTTxBtcUxto
		// 输出信息
		var outWithdrawRows []*model.DBTWithdraw
		// 计算交易
		for _, withdrawRow := range withdrawRows {
			var tmpInUxtoRows []*model.DBTTxBtcUxto
			// 添加输出
			withdrawBalance, err := decimal.NewFromString(withdrawRow.BalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			outBalance += withdrawBalance.Mul(decimal.NewFromInt(1e8)).IntPart()
			feeOutWithdrawRows := append(outWithdrawRows, withdrawRow)
			// 处理金额
			isInputBalanceOk := false
			for {
				var feeInUxtoRows []*model.DBTTxBtcUxto
				copy(feeInUxtoRows, inUxtoRows)
				if len(tmpInUxtoRows) > 0 {
					feeInUxtoRows = append(feeInUxtoRows, tmpInUxtoRows...)
				}
				txSize, err := BtcTxWithdrawSize(GetNetwork(xenv.Cfg.BtcNetworkType).Params, feeInUxtoRows, feeOutWithdrawRows, addressWifMap)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				if inBalance >= outBalance+txSize*feePriceValue {
					// input数额充足，继续添加withdraw
					isInputBalanceOk = true
					break
				}
				if txSize >= MaxTxSize {
					// 长度过长
					break
				}
				if uxtoUseIndex >= len(uxtoRows) {
					// 没有多余的input了
					break
				}
				uxtoRow := uxtoRows[uxtoUseIndex]
				uxtoBalance, err := decimal.NewFromString(uxtoRow.VoutValue)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				inBalance += uxtoBalance.Mul(decimal.NewFromInt(1e8)).IntPart()
				tmpInUxtoRows = append(tmpInUxtoRows, uxtoRow)
				uxtoUseIndex++
			}
			if isInputBalanceOk {
				// 处理这些数据
				outWithdrawRows = append(outWithdrawRows, withdrawRow)
				if len(tmpInUxtoRows) > 0 {
					inUxtoRows = append(inUxtoRows, tmpInUxtoRows...)
				}
			} else {
				break
			}
		}
		//mcommon.Log.Debugf("inUxtoRows: %#v, outWithdrawRows: %#v", inUxtoRows, outWithdrawRows)
		if len(inUxtoRows) == 0 {
			mcommon.Log.Errorf("btc hot balance limit")
			return
		}
		if len(outWithdrawRows) == 0 {
			mcommon.Log.Errorf("btc hot balance limit")
			return
		}
		// 创建交易
		var argVins []*StBtxTxIn
		var argVouts []*StBtxTxOut
		for _, vin := range inUxtoRows {
			wif, ok := addressWifMap[vin.VoutAddress]
			if !ok {
				mcommon.Log.Errorf("no wif of: %s", vin.VoutAddress)
				return
			}
			balance, err := decimal.NewFromString(vin.VoutValue)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			argVins = append(argVins, &StBtxTxIn{
				VinTxHash: vin.TxID,
				VinTxN:    vin.VoutN,
				VinScript: vin.VoutScript,
				Balance:   balance.Mul(decimal.NewFromInt(1e8)).IntPart(),
				Wif:       wif,
			})
		}
		for _, vout := range outWithdrawRows {
			balance, err := decimal.NewFromString(vout.BalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			argVouts = append(argVouts, &StBtxTxOut{
				VoutAddress: vout.ToAddress,
				Balance:     balance.Mul(decimal.NewFromInt(1e8)).IntPart(),
			})
		}
		tx, err := BtcMakeTx(GetNetwork(xenv.Cfg.BtcNetworkType).Params, argVins, argVouts, feePriceValue, hotAddress)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		b := new(bytes.Buffer)
		b.Grow(tx.SerializeSize())
		err = tx.Serialize(b)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		//mcommon.Log.Debugf("raw tx: %s", hex.EncodeToString(b.Bytes()))
		now := time.Now().Unix()
		var sendRows []*model.DBTSendBtc
		var updateUxtoRows []*model.DBTTxBtcUxto
		var updateWithdrawRows []*model.DBTWithdraw
		for i, outWithdrawRow := range outWithdrawRows {
			gas := int64(0)
			gasPrice := int64(0)
			sendHex := ""
			if i == 0 {
				sendHex = hex.EncodeToString(b.Bytes())
				gas = GetTxVsize(tx)
				gasPrice = feePriceValue
			}
			sendRows = append(sendRows, &model.DBTSendBtc{
				RelatedType:  app.SendRelationTypeWithdraw,
				RelatedID:    outWithdrawRow.ID,
				TokenID:      0,
				TxID:         tx.TxHash().String(),
				FromAddress:  hotAddress,
				ToAddress:    outWithdrawRow.ToAddress,
				BalanceReal:  outWithdrawRow.BalanceReal,
				Gas:          gas,
				GasPrice:     gasPrice,
				Hex:          sendHex,
				CreateTime:   now,
				HandleStatus: 0,
				HandleMsg:    "",
				HandleTime:   now,
			})
			updateWithdrawRows = append(updateWithdrawRows, &model.DBTWithdraw{
				ID:           outWithdrawRow.ID,
				TxHash:       fmt.Sprintf("%s_%d", tx.TxHash().String(), i),
				HandleStatus: app.WithdrawStatusHex,
				HandleMsg:    "hex",
				HandleTime:   now,
			})
		}
		for i, uxtoRow := range inUxtoRows {
			updateUxtoRows = append(updateUxtoRows, &model.DBTTxBtcUxto{
				ID:           uxtoRow.ID,
				TxID:         uxtoRow.TxID,
				VoutN:        uxtoRow.VoutN,
				SpendTxID:    tx.TxHash().String(),
				SpendN:       int64(i),
				HandleStatus: app.UxtoHandleStatusUse,
				HandleMsg:    "use",
				HandleTime:   now,
			})
		}
		// 插入数据
		_, err = model.SQLCreateManyTSendBtc(
			context.Background(),
			dbTx,
			sendRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新uxto状态
		_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
			context.Background(),
			dbTx,
			updateUxtoRows,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新withdraw
		_, err = app.SQLCreateManyTWithdrawUpdate(
			context.Background(),
			dbTx,
			updateWithdrawRows,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 提交事物
		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// CheckTxNotify 创建btc冲币通知
func CheckTxNotify() {
	lockKey := "BtcCheckTxNotify"
	app.LockWrap(lockKey, func() {
		txRows, err := app.SQLSelectTTxBtcColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTTxBtcID,
				model.DBColTTxBtcProductID,
				model.DBColTTxBtcTxID,
				model.DBColTTxBtcVoutAddress,
				model.DBColTTxBtcVoutN,
				model.DBColTTxBtcVoutValue,
			},
			app.TxStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		for _, txRow := range txRows {
			if !mcommon.IsIntInSlice(productIDs, txRow.ProductID) {
				productIDs = append(productIDs, txRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var notifyTxIDs []int64
		var notifyRows []*model.DBTProductNotify
		now := time.Now().Unix()
		for _, txRow := range txRows {
			productRow, ok := productMap[txRow.ProductID]
			if !ok {
				mcommon.Log.Warnf("no productMap: %d", txRow.ProductID)
				notifyTxIDs = append(notifyTxIDs, txRow.ID)
				continue
			}
			nonce := mcommon.GetUUIDStr()
			reqObj := gin.H{
				"tx_hash":     fmt.Sprintf("%s_%d", txRow.TxID, txRow.VoutN),
				"app_name":    productRow.AppName,
				"address":     txRow.VoutAddress,
				"balance":     txRow.VoutValue,
				"symbol":      CoinSymbol,
				"notify_type": app.NotifyTypeTx,
			}
			reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
			req, err := json.Marshal(reqObj)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
			notifyRows = append(notifyRows, &model.DBTProductNotify{
				Nonce:        nonce,
				ProductID:    txRow.ProductID,
				ItemType:     app.SendRelationTypeTx,
				ItemID:       txRow.ID,
				NotifyType:   app.NotifyTypeTx,
				TokenSymbol:  CoinSymbol,
				URL:          productRow.CbURL,
				Msg:          string(req),
				HandleStatus: app.NotifyStatusInit,
				HandleMsg:    "",
				CreateTime:   now,
				UpdateTime:   now,
			})
			notifyTxIDs = append(notifyTxIDs, txRow.ID)
		}
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTTxBtcStatusByIDs(
			context.Background(),
			xenv.DbCon,
			notifyTxIDs,
			model.DBTTxBtc{
				HandleStatus: app.TxStatusNotify,
				HandleMsg:    "notify",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})

}

// CheckGasPrice 检测gas price
func CheckGasPrice() {
	lockKey := "BtcCheckGasPrice"
	app.LockWrap(lockKey, func() {
		// 获取最高单价
		maxValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"max_gas_price_btc",
		)
		if err != nil {
			if !strings.Contains(err.Error(), "no app status int of") {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		if maxValue <= 0 {
			maxValue = 168
			// 创建
			_, err := model.SQLCreateTAppStatusInt(
				context.Background(),
				xenv.DbCon,
				&model.DBTAppStatusInt{
					K: "max_gas_price_btc",
					V: maxValue,
				},
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		type StRespGasPrice struct {
			FastestFee  int64 `json:"fastestFee"`
			HalfHourFee int64 `json:"halfHourFee"`
			HourFee     int64 `json:"hourFee"`
		}
		gresp, body, errs := gorequest.New().
			Proxy(xenv.Cfg.Proxy).
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
		var resp StRespGasPrice
		err = json.Unmarshal([]byte(body), &resp)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		toUserGasPrice := resp.FastestFee
		toColdGasPrice := resp.HalfHourFee
		if toUserGasPrice > maxValue {
			toUserGasPrice = maxValue
		}
		if toColdGasPrice > maxValue {
			toColdGasPrice = maxValue
		}
		_, err = app.SQLUpdateTAppStatusIntByK(
			context.Background(),
			xenv.DbCon,
			&model.DBTAppStatusInt{
				K: "to_user_gas_price_btc",
				V: toUserGasPrice,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTAppStatusIntByK(
			context.Background(),
			xenv.DbCon,
			&model.DBTAppStatusInt{
				K: "to_cold_gas_price_btc",
				V: toColdGasPrice,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

// OmniCheckBlockSeek 检测到账
func OmniCheckBlockSeek() {
	lockKey := "OmniCheckBlockSeek"
	app.LockWrap(lockKey, func() {
		// 获取配置 延迟确认数
		confirmValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"btc_block_confirm_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取状态 当前处理完成的最新的block number
		seekValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"omni_seek_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		rpcBlockNum, err := omniclient.RPCGetBlockCount()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		startI := seekValue + 1
		endI := rpcBlockNum - confirmValue + 1
		//mcommon.Log.Debugf("omni block seek %d->%d", startI, endI)
		if startI < endI {
			// 获取所有token
			var tokenIndexes []int64
			tokenMap := make(map[int64]*model.DBTAppConfigTokenBtc)
			tokenRows, err := app.SQLSelectTAppConfigTokenBtcColAll(
				context.Background(),
				xenv.DbCon,
				[]string{
					model.DBColTAppConfigTokenBtcID,
					model.DBColTAppConfigTokenBtcTokenIndex,
					model.DBColTAppConfigTokenBtcTokenSymbol,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, tokenRow := range tokenRows {
				tokenIndexes = append(tokenIndexes, tokenRow.TokenIndex)
				tokenMap[tokenRow.TokenIndex] = tokenRow
			}
			// 遍历获取需要查询的block信息
			for i := startI; i < endI; i++ {
				//mcommon.Log.Debugf("omni check block: %d", i)
				blockHash, err := omniclient.RPCGetBlockHash(i)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 一个block
				rpcBlock, err := omniclient.RPCGetBlockVerbose(blockHash)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 目标地址
				var toAddresses []string
				toAddressTxMap := make(map[string][]*omniclient.StTxResult)
				for _, rpcTx := range rpcBlock.Tx {
					isOmniTx := false
					omniInAddress := ""
					for _, vout := range rpcTx.Vout {
						if strings.HasPrefix(vout.ScriptPubKey.Hex, omniWithReturnHex) {
							isOmniTx = true
							break
						}
					}
					if isOmniTx {
						for _, vin := range rpcTx.Vin {
							vinAddresses, err := GetAddressesOfVin(
								GetNetwork(xenv.Cfg.BtcNetworkType).Params,
								vin,
							)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							if len(vinAddresses) > 0 {
								omniInAddress = strings.Join(vinAddresses, ",")
								break
							}
						}
						isExchanged := false
						for i := len(rpcTx.Vout) - 1; i >= 0; i-- {
							vout := rpcTx.Vout[i]
							if len(vout.ScriptPubKey.Addresses) > 0 {
								if !isExchanged && strings.Join(vout.ScriptPubKey.Addresses, ",") == omniInAddress {
									isExchanged = true
									continue
								}
								// 添加omni接受地址
								if len(vout.ScriptPubKey.Addresses) == 1 {
									toAddress := vout.ScriptPubKey.Addresses[0]
									if !mcommon.IsStringInSlice(toAddresses, toAddress) {
										toAddresses = append(toAddresses, toAddress)
									}
									toAddressTxMap[toAddress] = append(
										toAddressTxMap[toAddress],
										rpcTx,
									)
								}
								break
							}
						}
					}
				}
				// 从db中查询这些地址是否是冲币地址中的地址
				dbAddressRows, err := app.SQLSelectTAddressKeyColByAddress(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTAddressKeyAddress,
						model.DBColTAddressKeyUseTag,
					},
					toAddresses,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				now := time.Now().Unix()
				var txTokenRows []*model.DBTTxBtcToken
				for _, dbAddressRow := range dbAddressRows {
					if dbAddressRow.UseTag >= 0 {
						// 非fee和hot地址
						rpcTxes := toAddressTxMap[dbAddressRow.Address]
						for _, rpcTx := range rpcTxes {
							rpcTx, err := omniclient.RPCOmniGetTransaction(rpcTx.Txid)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							// type_int 0 Simple Send
							if rpcTx.TypeInt == 0 && rpcTx.Valid && rpcTx.Confirmations > 0 {
								tokenRow, ok := tokenMap[rpcTx.Propertyid]
								if ok {
									txTokenRows = append(txTokenRows, &model.DBTTxBtcToken{
										ProductID:    dbAddressRow.UseTag,
										TokenIndex:   rpcTx.Propertyid,
										TokenSymbol:  tokenRow.TokenSymbol,
										BlockHash:    rpcTx.Blockhash,
										TxID:         rpcTx.Txid,
										FromAddress:  rpcTx.Sendingaddress,
										ToAddress:    rpcTx.Referenceaddress,
										Value:        rpcTx.Amount,
										Blocktime:    rpcTx.Blocktime,
										CreateAt:     now,
										HandleStatus: 0,
										HandleMsg:    "",
										HandleAt:     0,
										OrgStatus:    0,
										OrgMsg:       "",
										OrgAt:        0,
									})
								}
							}
						}
					}
				}
				// 插入tx数据
				_, err = model.SQLCreateManyTTxBtcToken(
					context.Background(),
					xenv.DbCon,
					txTokenRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新block num
				_, err = app.SQLUpdateTAppStatusIntByKGreater(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "omni_seek_num",
						V: i,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("SQLUpdateTAppStatusIntByK err: [%T] %s", err, err.Error())
					return
				}
			}
		}
	})
}

// OmniCheckTxOrg 检测零钱整理
func OmniCheckTxOrg() {
	lockKey := "OmniCheckTxOrg"
	app.LockWrap(lockKey, func() {
		// 开始事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 查找需要整理的交易
		txRows, err := app.SQLSelectTTxBtcTokenColByOrgStatusForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxBtcTokenID,
				model.DBColTTxBtcTokenTokenIndex,
				model.DBColTTxBtcTokenTokenSymbol,
				model.DBColTTxBtcTokenTxID,
				model.DBColTTxBtcTokenFromAddress,
				model.DBColTTxBtcTokenToAddress,
				model.DBColTTxBtcTokenValue,
			},
			app.TxOrgStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		type stOrgItem struct {
			Address    string
			TokenIndex int64
			Balance    int64
			txRows     []*model.DBTTxBtcToken
		}
		var tokenIndexes []int64
		orgMap := make(map[string]*stOrgItem)
		var omniAddresses []string
		var keyAddresses []string
		for _, txRow := range txRows {
			key := fmt.Sprintf("%s_%d", txRow.ToAddress, txRow.TokenIndex)
			orgItem, ok := orgMap[key]
			if !ok {
				orgItem = &stOrgItem{
					Address:    txRow.ToAddress,
					TokenIndex: txRow.TokenIndex,
				}
				orgMap[key] = orgItem
			}
			balance, err := decimal.NewFromString(txRow.Value)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			orgItem.Balance += balance.Mul(decimal.NewFromInt(1e8)).IntPart()
			orgItem.txRows = append(orgItem.txRows, txRow)

			if !mcommon.IsStringInSlice(omniAddresses, txRow.ToAddress) {
				omniAddresses = append(omniAddresses, txRow.ToAddress)
			}
			if !mcommon.IsIntInSlice(tokenIndexes, txRow.TokenIndex) {
				tokenIndexes = append(tokenIndexes, txRow.TokenIndex)
			}
			if !mcommon.IsStringInSlice(keyAddresses, txRow.ToAddress) {
				keyAddresses = append(keyAddresses, txRow.ToAddress)
			}
		}
		if len(orgMap) > 0 {
			// 获取手续费配置
			feePriceValue, err := app.SQLGetTAppStatusIntValueByK(
				context.Background(),
				dbTx,
				"to_cold_gas_price_btc",
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			tokenMap := make(map[int64]*model.DBTAppConfigTokenBtc)
			var tokenFeeAddresses []string
			tokenRows, err := app.SQLSelectTAppConfigTokenBtcColByIndexes(
				context.Background(),
				dbTx,
				[]string{
					model.DBColTAppConfigTokenBtcID,
					model.DBColTAppConfigTokenBtcTokenIndex,
					model.DBColTAppConfigTokenBtcHotAddress,
					model.DBColTAppConfigTokenBtcFeeAddress,
					model.DBColTAppConfigTokenBtcColdAddress,
					model.DBColTAppConfigTokenBtcTxOrgMinBalance,
				},
				tokenIndexes,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, tokenRow := range tokenRows {
				tokenMap[tokenRow.TokenIndex] = tokenRow
				if !mcommon.IsStringInSlice(tokenFeeAddresses, tokenRow.FeeAddress) {
					tokenFeeAddresses = append(tokenFeeAddresses, tokenRow.FeeAddress)
				}
				if !mcommon.IsStringInSlice(keyAddresses, tokenRow.HotAddress) {
					keyAddresses = append(keyAddresses, tokenRow.HotAddress)
				}
				if !mcommon.IsStringInSlice(keyAddresses, tokenRow.FeeAddress) {
					keyAddresses = append(keyAddresses, tokenRow.FeeAddress)
				}
			}
			addressWifMap, err := GetWifMapByAddresses(
				context.Background(),
				dbTx,
				keyAddresses,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			omniUxtoMap := make(map[string][]*model.DBTTxBtcUxto)
			omniUxtoRows, err := app.SQLSelectTTxBtcUxtoColByAddressesAndTypeForUpdate(
				context.Background(),
				dbTx,
				[]string{
					model.DBColTTxBtcUxtoID,
					model.DBColTTxBtcUxtoTxID,
					model.DBColTTxBtcUxtoVoutN,
					model.DBColTTxBtcUxtoVoutAddress,
					model.DBColTTxBtcUxtoVoutValue,
					model.DBColTTxBtcUxtoVoutScript,
				},
				omniAddresses,
				app.UxtoTypeOmni,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, omniUxtoRow := range omniUxtoRows {
				omniUxtoMap[omniUxtoRow.VoutAddress] = append(
					omniUxtoMap[omniUxtoRow.VoutAddress],
					omniUxtoRow,
				)
			}
			omniHotUxtoMap := make(map[string][]*model.DBTTxBtcUxto)
			omniHotUxtoRows, err := app.SQLSelectTTxBtcUxtoColByAddressesAndTypeForUpdate(
				context.Background(),
				dbTx,
				[]string{
					model.DBColTTxBtcUxtoID,
					model.DBColTTxBtcUxtoTxID,
					model.DBColTTxBtcUxtoVoutN,
					model.DBColTTxBtcUxtoVoutAddress,
					model.DBColTTxBtcUxtoVoutValue,
					model.DBColTTxBtcUxtoVoutScript,
				},
				tokenFeeAddresses,
				app.UxtoTypeOmniOrgFee,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, omniHotUxtoRow := range omniHotUxtoRows {
				omniHotUxtoMap[omniHotUxtoRow.VoutAddress] = append(
					omniHotUxtoMap[omniHotUxtoRow.VoutAddress],
					omniHotUxtoRow,
				)
			}
			// 处理零钱整理
			var sendTxIDs []int64
			var sendRows []*model.DBTSendBtc
			var usedUxtoRows []*model.DBTTxBtcUxto
			now := time.Now().Unix()
			for _, orgItem := range orgMap {
				tokenRow, ok := tokenMap[orgItem.TokenIndex]
				if !ok {
					mcommon.Log.Errorf("no token: %d", orgItem.TokenIndex)
					break
				}
				omniUxtoRows, ok := omniUxtoMap[orgItem.Address]
				if !ok {
					mcommon.Log.Errorf("no omni uxto %s", orgItem.Address)
					break
				}
				omniHotUxtoRows, ok := omniHotUxtoMap[tokenRow.FeeAddress]
				if !ok {
					mcommon.Log.Errorf("omni org fee limit")
					break
				}
				//mcommon.Log.Debugf("omniUxtoRows: %#v, omniHotUxtoRows: %#v", omniUxtoRows, omniHotUxtoRows)
				if len(omniUxtoRows) <= 0 {
					mcommon.Log.Errorf("omni org sender uxto limit")
					break
				}
				if len(omniHotUxtoRows) <= 0 {
					mcommon.Log.Errorf("omni org fee limit")
					break
				}
				omniHotUxtoIndex := 0
				isOmniInputOK := false
				for {
					if omniHotUxtoIndex >= len(omniHotUxtoRows) {
						break
					}
					tmpUxtoHotRows := omniHotUxtoRows[:omniHotUxtoIndex+1]
					// 计算手续费
					txSize, err := GetEstimateTxSize(
						1+int64(omniHotUxtoIndex+1),
						2,
						true,
					)
					fee := txSize * feePriceValue
					//mcommon.Log.Debugf("fee: %d", fee)

					inBalance := int64(0)
					outBalance := int64(0)
					// 输入金额
					omniUxtoBalance, err := decimal.NewFromString(omniUxtoRows[0].VoutValue)
					if err != nil {
						mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
						return
					}
					inBalance += omniUxtoBalance.Mul(decimal.NewFromInt(1e8)).IntPart()
					for _, tmpUxtoHotRow := range tmpUxtoHotRows {
						balance, err := decimal.NewFromString(tmpUxtoHotRow.VoutValue)
						if err != nil {
							mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
							return
						}
						inBalance += balance.Mul(decimal.NewFromInt(1e8)).IntPart()
					}
					// 输出金额
					// omni 输出
					outBalance += MinNondustOutput
					if inBalance >= outBalance+fee {
						// 输入金额ok
						isOmniInputOK = true
						break
					}
					omniHotUxtoIndex++
				}
				if !isOmniInputOK {
					mcommon.Log.Errorf("omni org fee limit")
					break
				}
				// 生成交易
				tx, err := OmniTxMake(
					GetNetwork(xenv.Cfg.BtcNetworkType).Params,
					omniUxtoRows[0],
					tokenRow.ColdAddress,
					tokenRow.FeeAddress,
					tokenRow.TokenIndex,
					orgItem.Balance,
					feePriceValue,
					addressWifMap,
					omniHotUxtoRows[:omniHotUxtoIndex+1],
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				txSize := tx.SerializeSize()
				b := new(bytes.Buffer)
				b.Grow(txSize)
				err = tx.Serialize(b)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				//mcommon.Log.Debugf("raw tx: %s", hex.EncodeToString(b.Bytes()))
				for i, txRow := range orgItem.txRows {
					gas := int64(0)
					gasPrice := int64(0)
					txHex := ""
					balanceReal := "0"
					if i == 0 {
						gas = GetTxVsize(tx)
						gasPrice = feePriceValue
						txHex = hex.EncodeToString(b.Bytes())
						balanceReal = decimal.NewFromInt(orgItem.Balance).Div(decimal.NewFromInt(1e8)).String()
					}
					// 发送数据
					sendRows = append(sendRows, &model.DBTSendBtc{
						RelatedType:  app.SendRelationTypeOmniOrg,
						RelatedID:    txRow.ID,
						TokenID:      txRow.TokenIndex,
						TxID:         tx.TxHash().String(),
						FromAddress:  txRow.ToAddress,
						ToAddress:    tokenRow.ColdAddress,
						BalanceReal:  balanceReal,
						Gas:          gas,
						GasPrice:     gasPrice,
						Hex:          txHex,
						CreateTime:   now,
						HandleStatus: 0,
						HandleMsg:    "",
						HandleTime:   0,
					})
					// 发送的t_tx_btc_token.id
					sendTxIDs = append(sendTxIDs, txRow.ID)
				}
				// 使用过的uxto
				tmpUxtoRow := omniUxtoRows[0]
				usedUxtoRows = append(usedUxtoRows, &model.DBTTxBtcUxto{
					ID:           tmpUxtoRow.ID,
					TxID:         tmpUxtoRow.TxID,
					VoutN:        tmpUxtoRow.VoutN,
					SpendTxID:    tx.TxHash().String(),
					SpendN:       0,
					HandleStatus: app.UxtoHandleStatusUse,
					HandleMsg:    "use",
					HandleTime:   now,
				})
				for i, usedUxtoRow := range omniHotUxtoRows[:omniHotUxtoIndex+1] {
					usedUxtoRows = append(usedUxtoRows, &model.DBTTxBtcUxto{
						ID:           usedUxtoRow.ID,
						TxID:         usedUxtoRow.TxID,
						VoutN:        usedUxtoRow.VoutN,
						SpendTxID:    tx.TxHash().String(),
						SpendN:       int64(i) + 1,
						HandleStatus: app.UxtoHandleStatusUse,
						HandleMsg:    "use",
						HandleTime:   now,
					})
				}
				// 重置数据
				omniUxtoMap[orgItem.Address] = omniUxtoRows[1:]
				omniHotUxtoMap[tokenRow.FeeAddress] = omniHotUxtoRows[omniHotUxtoIndex+1:]
			}
			// 添加发送
			_, err = model.SQLCreateManyTSendBtc(
				context.Background(),
				dbTx,
				sendRows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 更新uxto状态
			_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
				context.Background(),
				dbTx,
				usedUxtoRows,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 更新整理状态
			_, err = app.SQLUpdateTTxBtcTokenOrgStatusByIDs(
				context.Background(),
				dbTx,
				sendTxIDs,
				model.DBTTxBtcToken{
					OrgStatus: app.TxOrgStatusHex,
					OrgMsg:    "hex",
					OrgAt:     now,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// OmniCheckWithdraw 检测提现
func OmniCheckWithdraw() {
	lockKey := "OmniCheckWithdraw"
	app.LockWrap(lockKey, func() {
		var symbols []string
		var tokenHotAddresses []string
		tokenMap := make(map[string]*model.DBTAppConfigTokenBtc)
		tokenHotBalance := make(map[int64]int64)
		tokenBtcRows, err := app.SQLSelectTAppConfigTokenBtcColAll(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTAppConfigTokenBtcID,
				model.DBColTAppConfigTokenBtcTokenIndex,
				model.DBColTAppConfigTokenBtcTokenSymbol,
				model.DBColTAppConfigTokenBtcHotAddress,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		for _, tokenRow := range tokenBtcRows {
			symbols = append(symbols, tokenRow.TokenSymbol)
			tokenMap[tokenRow.TokenSymbol] = tokenRow
			if !mcommon.IsStringInSlice(tokenHotAddresses, tokenRow.HotAddress) {
				tokenHotAddresses = append(tokenHotAddresses, tokenRow.HotAddress)
			}
			balanceRealStr, err := omniclient.RPCOmniGetBalance(
				tokenRow.HotAddress,
				tokenRow.TokenIndex,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			balance, err := RealStrToBalanceInt64(balanceRealStr.Balance)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			pendingRealStr, err := app.SQLGetTSendBtcPendingBalanceReal(
				context.Background(),
				xenv.DbCon,
				tokenRow.HotAddress,
				tokenRow.TokenIndex,
			)
			pending, err := RealStrToBalanceInt64(pendingRealStr)
			balance -= pending
			tokenHotBalance[tokenRow.TokenIndex] = balance
		}
		// 开始事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 获取提币信息
		withdrawRows, err := app.SQLSelectTWithdrawColByStatusForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawSymbol,
				model.DBColTWithdrawBalanceReal,
			},
			app.WithdrawStatusInit,
			symbols,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(withdrawRows) == 0 {
			return
		}
		// 获取手续费配置
		feePriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			dbTx,
			"to_user_gas_price_btc",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取私钥
		addressWifMap, err := GetWifMapByAddresses(
			context.Background(),
			dbTx,
			tokenHotAddresses,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		omniHotUxtoMap := make(map[string][]*model.DBTTxBtcUxto)
		omniHotUxtoRows, err := app.SQLSelectTTxBtcUxtoColByAddressesAndTypeForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxBtcUxtoID,
				model.DBColTTxBtcUxtoTxID,
				model.DBColTTxBtcUxtoVoutN,
				model.DBColTTxBtcUxtoVoutAddress,
				model.DBColTTxBtcUxtoVoutValue,
				model.DBColTTxBtcUxtoVoutScript,
			},
			tokenHotAddresses,
			app.UxtoTypeOmniHot,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		for _, omniHotUxtoRow := range omniHotUxtoRows {
			omniHotUxtoMap[omniHotUxtoRow.VoutAddress] = append(
				omniHotUxtoMap[omniHotUxtoRow.VoutAddress],
				omniHotUxtoRow,
			)
		}
		var sendRows []*model.DBTSendBtc
		var updateUxtoRows []*model.DBTTxBtcUxto
		var updateWithdraws []*model.DBTWithdraw
		now := time.Now().Unix()
		for _, withdrawRow := range withdrawRows {
			tokenRow, ok := tokenMap[withdrawRow.Symbol]
			if !ok {
				mcommon.Log.Errorf("no token: %s", withdrawRow.Symbol)
				return
			}
			// 检测token余额
			withdrawBalance, err := RealStrToBalanceInt64(withdrawRow.BalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			tmp := tokenHotBalance[tokenRow.TokenIndex] - withdrawBalance
			if tmp < 0 {
				mcommon.Log.Errorf("omni token balance limit %d", tokenRow.TokenIndex)
				continue
			}
			tokenHotBalance[tokenRow.TokenIndex] -= withdrawBalance
			omniHotUxtoRows, ok := omniHotUxtoMap[tokenRow.HotAddress]
			if !ok {
				mcommon.Log.Errorf("no omni hot %s", tokenRow.HotAddress)
				return
			}
			//mcommon.Log.Debugf("omniHotUxtoRows: %#v", omniHotUxtoRows)
			if len(omniHotUxtoRows) <= 0 {
				mcommon.Log.Errorf("no omni hot uxto limit %d", withdrawRow.ID)
				continue
			}
			omniHotUxtoIndex := 0
			isOmniInputOK := false
			for {
				if omniHotUxtoIndex >= len(omniHotUxtoRows) {
					break
				}
				tmpUxtoHotRows := omniHotUxtoRows[:omniHotUxtoIndex+1]
				// 计算手续费
				txSize, err := GetEstimateTxSize(
					int64(omniHotUxtoIndex+1),
					2,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				fee := txSize * feePriceValue
				//mcommon.Log.Debugf("fee: %d", fee)

				inBalance := int64(0)
				outBalance := int64(0)
				// 输入金额
				for _, tmpUxtoHotRow := range tmpUxtoHotRows {
					balance, err := decimal.NewFromString(tmpUxtoHotRow.VoutValue)
					if err != nil {
						mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
						return
					}
					inBalance += balance.Mul(decimal.NewFromInt(1e8)).IntPart()
				}
				// 输出金额
				// omni 输出
				outBalance += MinNondustOutput
				if inBalance >= outBalance+fee {
					// 输入金额ok
					isOmniInputOK = true
					break
				}
				omniHotUxtoIndex++
			}
			if !isOmniInputOK {
				mcommon.Log.Errorf("omni withdraw fee limit")
				break
			}
			// 生成交易
			balance, err := decimal.NewFromString(withdrawRow.BalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			tx, err := OmniTxMake(
				GetNetwork(xenv.Cfg.BtcNetworkType).Params,
				omniHotUxtoRows[0],
				withdrawRow.ToAddress,
				tokenRow.HotAddress,
				tokenRow.TokenIndex,
				balance.Mul(decimal.NewFromInt(1e8)).IntPart(),
				feePriceValue,
				addressWifMap,
				omniHotUxtoRows[1:omniHotUxtoIndex+1],
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			txSize := tx.SerializeSize()
			b := new(bytes.Buffer)
			b.Grow(txSize)
			err = tx.Serialize(b)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			//mcommon.Log.Debugf("raw tx: %s", hex.EncodeToString(b.Bytes()))
			// 准备数据
			// 发送数据
			sendRows = append(sendRows, &model.DBTSendBtc{
				RelatedType:  app.SendRelationTypeWithdraw,
				RelatedID:    withdrawRow.ID,
				TokenID:      tokenRow.TokenIndex,
				TxID:         tx.TxHash().String(),
				FromAddress:  tokenRow.HotAddress,
				ToAddress:    withdrawRow.ToAddress,
				BalanceReal:  withdrawRow.BalanceReal,
				Gas:          GetTxVsize(tx),
				GasPrice:     feePriceValue,
				Hex:          hex.EncodeToString(b.Bytes()),
				CreateTime:   now,
				HandleStatus: 0,
				HandleMsg:    "",
				HandleTime:   0,
			})
			// 更新uxto
			for i, uxtoRow := range omniHotUxtoRows[:omniHotUxtoIndex+1] {
				updateUxtoRows = append(updateUxtoRows, &model.DBTTxBtcUxto{
					ID:           uxtoRow.ID,
					TxID:         uxtoRow.TxID,
					VoutN:        uxtoRow.VoutN,
					SpendTxID:    tx.TxHash().String(),
					SpendN:       int64(i),
					HandleStatus: app.UxtoHandleStatusUse,
					HandleMsg:    "use",
					HandleTime:   now,
				})
			}
			// 更新withdraw
			updateWithdraws = append(updateWithdraws, &model.DBTWithdraw{
				ID:           withdrawRow.ID,
				TxHash:       tx.TxHash().String(),
				HandleStatus: app.WithdrawStatusHex,
				HandleMsg:    "hex",
				HandleTime:   now,
			})
			// 重置数据
			omniHotUxtoMap[tokenRow.HotAddress] = omniHotUxtoRows[omniHotUxtoIndex+1:]
		}
		// 插入发送
		_, err = model.SQLCreateManyTSendBtc(
			context.Background(),
			dbTx,
			sendRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新uxto
		_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
			context.Background(),
			dbTx,
			updateUxtoRows,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新提币
		_, err = app.SQLCreateManyTWithdrawUpdate(
			context.Background(),
			dbTx,
			updateWithdraws,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 提交事物
		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// OmniCheckTxNotify 创建omni冲币通知
func OmniCheckTxNotify() {
	lockKey := "OmniCheckTxNotify"
	app.LockWrap(lockKey, func() {
		txRows, err := app.SQLSelectTTxBtcTokenColByHandleStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTTxBtcTokenID,
				model.DBColTTxBtcTokenProductID,
				model.DBColTTxBtcTokenTokenIndex,
				model.DBColTTxBtcTokenTokenSymbol,
				model.DBColTTxBtcTokenTxID,
				model.DBColTTxBtcTokenFromAddress,
				model.DBColTTxBtcTokenToAddress,
				model.DBColTTxBtcTokenValue,
			},
			app.TxStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		for _, txRow := range txRows {
			if !mcommon.IsIntInSlice(productIDs, txRow.ProductID) {
				productIDs = append(productIDs, txRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var notifyTxIDs []int64
		var notifyRows []*model.DBTProductNotify
		now := time.Now().Unix()
		for _, txRow := range txRows {
			productRow, ok := productMap[txRow.ProductID]
			if !ok {
				mcommon.Log.Warnf("no productMap: %d", txRow.ProductID)
				notifyTxIDs = append(notifyTxIDs, txRow.ID)
				continue
			}
			nonce := mcommon.GetUUIDStr()
			reqObj := gin.H{
				"tx_hash":     txRow.TxID,
				"app_name":    productRow.AppName,
				"address":     txRow.ToAddress,
				"balance":     txRow.Value,
				"symbol":      txRow.TokenSymbol,
				"notify_type": app.NotifyTypeTx,
			}
			reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
			req, err := json.Marshal(reqObj)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
			notifyRows = append(notifyRows, &model.DBTProductNotify{
				Nonce:        nonce,
				ProductID:    txRow.ProductID,
				ItemType:     app.SendRelationTypeTx,
				ItemID:       txRow.ID,
				NotifyType:   app.NotifyTypeTx,
				TokenSymbol:  txRow.TokenSymbol,
				URL:          productRow.CbURL,
				Msg:          string(req),
				HandleStatus: app.NotifyStatusInit,
				HandleMsg:    "",
				CreateTime:   now,
				UpdateTime:   now,
			})
			notifyTxIDs = append(notifyTxIDs, txRow.ID)
		}
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTTxBtcTokenHandleStatusByIDs(
			context.Background(),
			xenv.DbCon,
			notifyTxIDs,
			model.DBTTxBtcToken{
				HandleStatus: app.TxStatusNotify,
				HandleMsg:    "notify",
				HandleAt:     now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})

}

// CheckBlockSeekHotAndFee 检测到账
func CheckBlockSeekHotAndFee() {
	lockKey := "BtcCheckBlockSeekHotAndFee"
	app.LockWrap(lockKey, func() {
		// 获取状态 当前处理完成的最新的block number
		seekValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"btc_hot_fee_seek_num",
		)
		if err != nil {
			if strings.Contains(err.Error(), "no app status int of") {
				rpcBlockNum, err := omniclient.RPCGetBlockCount()
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				_, err = model.SQLCreateTAppStatusInt(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "btc_hot_fee_seek_num",
						V: rpcBlockNum,
					},
					true,
				)
			} else {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		rpcBlockNum, err := omniclient.RPCGetBlockCount()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		startI := seekValue + 1
		endI := rpcBlockNum + 1
		if startI < endI {
			// 获取btc热钱包地址
			hotAddress, err := app.SQLGetTAppConfigStrValueByK(
				context.Background(),
				xenv.DbCon,
				"hot_wallet_address_btc",
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 获取所有token
			var tokenHotAddresses []string
			var tokenFeeAddresses []string
			tokenRows, err := app.SQLSelectTAppConfigTokenBtcColAll(
				context.Background(),
				xenv.DbCon,
				[]string{
					model.DBColTAppConfigTokenBtcID,
					model.DBColTAppConfigTokenBtcHotAddress,
					model.DBColTAppConfigTokenBtcFeeAddress,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, tokenRow := range tokenRows {
				if !mcommon.IsStringInSlice(tokenHotAddresses, tokenRow.HotAddress) {
					tokenHotAddresses = append(tokenHotAddresses, tokenRow.HotAddress)
				}
				if !mcommon.IsStringInSlice(tokenFeeAddresses, tokenRow.FeeAddress) {
					tokenFeeAddresses = append(tokenFeeAddresses, tokenRow.FeeAddress)
				}
			}
			// 遍历获取需要查询的block信息
			for curBlockNum := startI; curBlockNum < endI; curBlockNum++ {
				blockHash, err := omniclient.RPCGetBlockHash(curBlockNum)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 一个block
				rpcBlock, err := omniclient.RPCGetBlockVerbose(blockHash)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 所有输入数据
				var vinTxHashes []string
				type StVinWithIndex struct {
					TxHash string
					VoutN  int64

					SpendTxHash string
					SpendN      int64
				}
				vinMap := make(map[string]*StVinWithIndex)
				// 待插入uxto
				var txBtcUxtoRows []*model.DBTTxBtcUxto
				now := time.Now().Unix()
				// 所有tx
				for _, rpcTx := range rpcBlock.Tx {
					// 每个tx
					// 记录输入tx hash
					for i, vin := range rpcTx.Vin {
						fromTxHash := vin.Txid
						if !mcommon.IsStringInSlice(vinTxHashes, fromTxHash) {
							vinTxHashes = append(vinTxHashes, fromTxHash)
						}
						key := fmt.Sprintf("%s-%d", vin.Txid, vin.Vout)
						vinMap[key] = &StVinWithIndex{
							TxHash:      vin.Txid,
							VoutN:       vin.Vout,
							SpendTxHash: rpcTx.Txid,
							SpendN:      int64(i),
						}
					}
					// 检测是否需要关注输出
					isIgnoreTx := true
					for _, vout := range rpcTx.Vout {
						if len(vout.ScriptPubKey.Addresses) == 1 {
							// 输出地址
							toAddress := vout.ScriptPubKey.Addresses[0]
							if toAddress == hotAddress {
								// btc 热钱包地址
								isIgnoreTx = false
								break
							} else if mcommon.IsStringInSlice(tokenFeeAddresses, toAddress) ||
								mcommon.IsStringInSlice(tokenHotAddresses, toAddress) {
								// token hot and fee
								isIgnoreTx = false
								break
							}
						}
					}
					if isIgnoreTx {
						continue
					}
					// 输出
					omniScript := omniWithReturnHex
					isOmniTx := false
					omniIndex := int64(-1)
					omniInAddress := ""
					// 检测是否是omni交易
					for _, vout := range rpcTx.Vout {
						if strings.HasPrefix(vout.ScriptPubKey.Hex, omniScript) {
							isOmniTx = true
							break
						}
					}
					// 获取omni输出索引
					if isOmniTx {
						for _, vin := range rpcTx.Vin {
							vinAddresses, err := GetAddressesOfVin(
								GetNetwork(xenv.Cfg.BtcNetworkType).Params,
								vin,
							)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							if len(vinAddresses) > 0 {
								omniInAddress = strings.Join(vinAddresses, ",")
								break
							}
						}
						isExchanged := false
						for i := len(rpcTx.Vout) - 1; i >= 0; i-- {
							vout := rpcTx.Vout[i]
							if len(vout.ScriptPubKey.Addresses) > 0 {
								if !isExchanged && strings.Join(vout.ScriptPubKey.Addresses, ",") == omniInAddress {
									isExchanged = true
									continue
								}
								omniIndex = vout.N
								break
							}
						}
					}
					for _, vout := range rpcTx.Vout {
						if len(vout.ScriptPubKey.Addresses) == 1 {
							// 输出地址
							uxtoType := -1
							toAddress := vout.ScriptPubKey.Addresses[0]
							if toAddress == hotAddress {
								// btc 热钱包地址
								uxtoType = app.UxtoTypeHot
							} else if mcommon.IsStringInSlice(tokenFeeAddresses, toAddress) ||
								mcommon.IsStringInSlice(tokenHotAddresses, toAddress) {
								if vout.N == omniIndex {
									// omni 交易
									uxtoType = app.UxtoTypeOmni
								} else {
									if mcommon.IsStringInSlice(tokenFeeAddresses, toAddress) {
										// 整理手续费地址
										uxtoType = app.UxtoTypeOmniOrgFee
									} else if mcommon.IsStringInSlice(tokenHotAddresses, toAddress) {
										// 提币手续费地址
										uxtoType = app.UxtoTypeOmniHot
									}
								}
							}
							if uxtoType > 0 {
								voutScript := vout.ScriptPubKey.Hex
								value := decimal.NewFromFloat(vout.Value).String()
								txBtcUxtoRows = append(
									txBtcUxtoRows,
									&model.DBTTxBtcUxto{
										UxtoType:     int64(uxtoType),
										BlockHash:    rpcBlock.Hash,
										TxID:         rpcTx.Txid,
										VoutN:        vout.N,
										VoutAddress:  toAddress,
										VoutValue:    value,
										VoutScript:   voutScript,
										CreateTime:   now,
										SpendTxID:    "",
										SpendN:       0,
										HandleStatus: 0,
										HandleMsg:    "",
										HandleTime:   now,
									},
								)
							}
						}
					}
				}
				// 从uxto中查询txhash
				var updateUxtoRows []*model.DBTTxBtcUxto
				uxtoRows, err := app.SQLSelectTTxBtcUxtoColByTxIDs(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTTxBtcUxtoID,
						model.DBColTTxBtcUxtoTxID,
						model.DBColTTxBtcUxtoVoutN,
					},
					vinTxHashes,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				for _, uxtoRow := range uxtoRows {
					key := fmt.Sprintf("%s-%d", uxtoRow.TxID, uxtoRow.VoutN)
					rpcVin, ok := vinMap[key]
					if ok {
						updateUxtoRows = append(updateUxtoRows, &model.DBTTxBtcUxto{
							ID:           uxtoRow.ID,
							TxID:         uxtoRow.TxID,
							VoutN:        uxtoRow.VoutN,
							SpendTxID:    rpcVin.SpendTxHash,
							SpendN:       rpcVin.SpendN,
							HandleStatus: app.UxtoHandleStatusConfirm,
							HandleMsg:    "confirmed",
							HandleTime:   now,
						})
					}
				}
				// 创建uxto
				_, err = model.SQLCreateManyTTxBtcUxto(
					context.Background(),
					xenv.DbCon,
					txBtcUxtoRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新uxto状态
				_, err = app.SQLCreateManyTTxBtcUxtoUpdate(
					context.Background(),
					xenv.DbCon,
					updateUxtoRows,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新block num
				_, err = app.SQLUpdateTAppStatusIntByKGreater(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "btc_hot_fee_seek_num",
						V: curBlockNum,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("SQLUpdateTAppStatusIntByK err: [%T] %s", err, err.Error())
					return
				}
			}
		}
	})
}
