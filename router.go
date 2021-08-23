package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/boltdb/bolt"
	"github.com/driver005/payment/constant"
	"github.com/driver005/payment/dto"
	"github.com/driver005/payment/model"
	"github.com/julienschmidt/httprouter"
)

var db *bolt.DB

func setupRouter(dbConfig *bolt.DB) *httprouter.Router {

	// Database Config
	db = dbConfig

	// Routes
	router := httprouter.New()

	// Payments Routes
	router.POST("/v1/payments/authorization", PaymentsAuthorization)
	router.POST("/v1/payments/capture/:authorization_id", PaymentsCapture)
	router.POST("/v1/payments/reversal/:authorization_id", PaymentsReversal)
	router.POST("/v1/payments/refund/:capture_id", PaymentsRefund)

	// Accounts Routes
	router.GET("/v1/accounts/create", AccountsCreate)
	router.POST("/v1/accounts/deposit", AccountsDeposit)
	router.POST("/v1/accounts/detail", AccountsDetail)
	router.POST("/v1/accounts/statement", AccountsStatement)

	return router
}

func PaymentsAuthorization(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var authorizationRequestDto dto.AuthorizationRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&authorizationRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Prepare Payment Response
	w.Header().Set("content-type", "application/json")

	// Basic Validation - Business Account
	var businessAccountId = r.Header.Get("From")
	var businessAccount model.Account
	if len(businessAccountId) > 0 {
		businessAccount, _ = getAccount(db, businessAccountId)
	} else {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(authorizationRequestDto.OrderId, "3", "Invalid Merchant"))
		return
	}
	if businessAccount.Id != businessAccountId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(authorizationRequestDto.OrderId, "15", "No Such Issuer"))
		return
	}

	// Basic Validation - Personal Account
	var personalAccountId = fmt.Sprintf("%v", authorizationRequestDto.CardNumber)
	var personalAccount model.Account
	if len(personalAccountId) > 0 {
		personalAccount, _ = getAccount(db, personalAccountId)
	} else {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(authorizationRequestDto.OrderId, "12", "Invalid Card Number"))
		return
	}
	if personalAccount.Id != personalAccountId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(authorizationRequestDto.OrderId, "56", "No Card Record"))
		return
	}

	if personalAccount.CardNumber != authorizationRequestDto.CardNumber ||
		personalAccount.CardSecurityCode != authorizationRequestDto.CardSecurityCode ||
		personalAccount.CardExpiryYear != authorizationRequestDto.CardExpiryYear ||
		personalAccount.CardExpiryMonth != authorizationRequestDto.CardExpiryMonth {
		var payment = model.CreateAuthorizationPayment(authorizationRequestDto,
			personalAccount,
			businessAccount,
			"5",
			"Do Not Honour")
		savePayment(db, payment)
		businessAccount.Statement = append(businessAccount.Statement, payment.Id)
		saveAccount(db, businessAccount)
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(payment.Id, "5", "Do Not Honour"))
		return
	}

	if personalAccount.Available < authorizationRequestDto.Amount {
		var payment = model.CreateAuthorizationPayment(authorizationRequestDto,
			personalAccount,
			businessAccount,
			"51",
			"Insufficient Funds")
		savePayment(db, payment)
		businessAccount.Statement = append(businessAccount.Statement, payment.Id)
		saveAccount(db, businessAccount)
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(payment.Id, "51", "Insufficient Funds"))
		return
	}

	// Successful Payment
	personalAccount.Available = personalAccount.Available - authorizationRequestDto.Amount
	personalAccount.Blocked = personalAccount.Blocked + authorizationRequestDto.Amount
	saveAccount(db, personalAccount)
	businessAccount.Blocked = businessAccount.Blocked + authorizationRequestDto.Amount
	saveAccount(db, businessAccount)
	var payment = model.CreateAuthorizationPayment(authorizationRequestDto,
		personalAccount,
		businessAccount,
		"0",
		"Approved")
	savePayment(db, payment)
	businessAccount.Statement = append(businessAccount.Statement, payment.Id)
	saveAccount(db, businessAccount)
	personalAccount.Statement = append(personalAccount.Statement, payment.Id)
	saveAccount(db, personalAccount)
	json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(payment.Id, "0", "Approved"))
}

func PaymentsCapture(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	var successiveRequestDto dto.SuccessiveRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&successiveRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("content-type", "application/json")

	// Basic Validation - Business Account
	var businessAccountId = r.Header.Get("From")
	var referenceId = p.ByName("authorization_id")
	var businessAccount model.Account
	if len(businessAccountId) > 0 {
		businessAccount, _ = getAccount(db, businessAccountId)
	} else {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "3", "Invalid Merchant"))
		return
	}
	if businessAccount.Id != businessAccountId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "15", "No Such Issuer"))
		return
	}

	// Check if previous payment exists
	successiveRequestDto.Type = constant.CAPTURE
	successiveRequestDto.ReferenceId = referenceId
	if len(referenceId) <= 0 {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}
	var referencedPayment, _ = getPayment(db, referenceId)
	if referencedPayment.Id != referenceId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}

	// Create successive payment
	var successivePayment model.Payment
	if referencedPayment.Operation == constant.AUTHORIZATION && referencedPayment.Status == "0" {
		if referencedPayment.CurrentAmount < successiveRequestDto.Amount {
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"13",
				"Invalid Amount")
			savePayment(db, successivePayment)
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"13",
				"Invalid Amount"))
			return
		} else {
			referencedPayment.CurrentAmount = referencedPayment.CurrentAmount - successiveRequestDto.Amount
			savePayment(db, referencedPayment)
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"0",
				"Approved")
			savePayment(db, successivePayment)
			var personalAccountId = fmt.Sprintf("%v", referencedPayment.CardNumber)
			var personalAccount, _ = getAccount(db, personalAccountId)
			personalAccount.Blocked = personalAccount.Blocked - successiveRequestDto.Amount
			personalAccount.Statement = append(personalAccount.Statement, successivePayment.Id)
			saveAccount(db, personalAccount)
			businessAccount.Blocked = businessAccount.Blocked - successiveRequestDto.Amount
			businessAccount.Available = businessAccount.Available + successiveRequestDto.Amount
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"0",
				"Approved"))
			return
		}

	}

	json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id, "12", "Invalid Transaction"))
}

func PaymentsReversal(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	var successiveRequestDto dto.SuccessiveRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&successiveRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("content-type", "application/json")

	// Basic Validation - Business Account
	var referenceId = p.ByName("authorization_id")
	var businessAccountId = r.Header.Get("From")
	var businessAccount model.Account
	if len(businessAccountId) > 0 {
		businessAccount, _ = getAccount(db, businessAccountId)
	} else {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "3", "Invalid Merchant"))
		return
	}
	if businessAccount.Id != businessAccountId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "15", "No Such Issuer"))
		return
	}

	// Basic Validation - Payment
	successiveRequestDto.Type = constant.REVERSAL
	successiveRequestDto.ReferenceId = referenceId
	if len(referenceId) <= 0 {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}
	var referencedPayment, _ = getPayment(db, referenceId)
	if referencedPayment.Id != successiveRequestDto.ReferenceId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}

	// Create Successive Payment
	var successivePayment model.Payment
	if referencedPayment.Operation == constant.AUTHORIZATION && referencedPayment.Status == "0" {
		if referencedPayment.CurrentAmount < successiveRequestDto.Amount {
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"13",
				"Invalid Amount")
			savePayment(db, successivePayment)
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"13",
				"Invalid Amount"))
			return
		} else {
			referencedPayment.CurrentAmount = referencedPayment.CurrentAmount - successiveRequestDto.Amount
			savePayment(db, referencedPayment)
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"0",
				"Approved")
			savePayment(db, successivePayment)
			var personalAccountId = fmt.Sprintf("%v", referencedPayment.CardNumber)
			var personalAccount, _ = getAccount(db, personalAccountId)
			personalAccount.Blocked = personalAccount.Blocked - successiveRequestDto.Amount
			personalAccount.Available = personalAccount.Available + successiveRequestDto.Amount
			personalAccount.Statement = append(personalAccount.Statement, successivePayment.Id)
			saveAccount(db, personalAccount)
			businessAccount.Blocked = businessAccount.Blocked - successiveRequestDto.Amount
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"0",
				"Approved"))
			return
		}

	}

	json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id, "12", "Invalid Transaction"))
}

func PaymentsRefund(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	var successiveRequestDto dto.SuccessiveRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&successiveRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("content-type", "application/json")

	// Basic Validation - Business Account
	var referenceId = p.ByName("capture_id")
	var businessAccountId = r.Header.Get("From")
	var businessAccount model.Account
	if len(businessAccountId) > 0 {
		businessAccount, _ = getAccount(db, businessAccountId)
	} else {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "3", "Invalid Merchant"))
		return
	}
	if businessAccount.Id != businessAccountId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "15", "No Such Issuer"))
		return
	}

	// Basic Validation - Payment
	successiveRequestDto.Type = constant.REFUND
	successiveRequestDto.ReferenceId = referenceId
	if len(referenceId) <= 0 {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}
	var referencedPayment, _ = getPayment(db, referenceId)
	if referencedPayment.Id != successiveRequestDto.ReferenceId {
		json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(referenceId, "12", "Invalid Transaction	"))
		return
	}

	// Create Successive Payment
	var successivePayment model.Payment
	if referencedPayment.Operation == constant.CAPTURE && referencedPayment.Status == "0" {
		if referencedPayment.CurrentAmount < successiveRequestDto.Amount {
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"13",
				"Invalid Amount")
			savePayment(db, successivePayment)
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"13",
				"Invalid Amount"))
			return
		} else {
			referencedPayment.CurrentAmount = referencedPayment.CurrentAmount - successiveRequestDto.Amount
			savePayment(db, referencedPayment)
			successivePayment = model.CreateSuccessivePayment(successiveRequestDto,
				referencedPayment,
				"0",
				"Approved")
			savePayment(db, successivePayment)
			var personalAccountId = fmt.Sprintf("%v", referencedPayment.CardNumber)
			var personalAccount, _ = getAccount(db, personalAccountId)
			personalAccount.Available = personalAccount.Available + successiveRequestDto.Amount
			personalAccount.Statement = append(personalAccount.Statement, successivePayment.Id)
			saveAccount(db, personalAccount)
			businessAccount.Available = businessAccount.Available - successiveRequestDto.Amount
			businessAccount.Statement = append(businessAccount.Statement, successivePayment.Id)
			saveAccount(db, businessAccount)
			json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id,
				"0",
				"Approved"))
			return
		}

	}

	json.NewEncoder(w).Encode(dto.CreatePaymentResponseDto(successivePayment.Id, "12", "Invalid Transaction"))
}

func AccountsCreate(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {

	// Create Account
	var account = model.GenerateAccount()
	saveAccount(db, account)
	account.Statement = nil

	// Reply with Account Details
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(account)
}

func AccountsDeposit(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	var accountRequestDto dto.AccountRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&accountRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Validate Account
	var accountResponseDto = dto.CreateAccountResponseDto(accountRequestDto.Id, "14", "Invalid Card Number")
	if len(accountRequestDto.Id) > 0 {
		var account, _ = getAccount(db, accountRequestDto.Id)
		if len(account.Id) > 0 {
			account.Available = account.Available + accountRequestDto.Amount
			saveAccount(db, account)
			accountResponseDto = dto.CreateAccountResponseDto(accountRequestDto.Id, "0", "Approved")
		}
	}

	// Reply with Account Operation
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(accountResponseDto)
}

func AccountsDetail(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var accountRequestDto dto.AccountRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&accountRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Validate Account
	var account model.Account
	if len(accountRequestDto.Id) > 0 {
		account, _ = getAccount(db, accountRequestDto.Id)
	}

	// Reply with Account Operation
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(account)
}

func AccountsStatement(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var accountRequestDto dto.AccountRequestDto

	// Body to json
	if err := json.NewDecoder(r.Body).Decode(&accountRequestDto); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Validate Account
	var accountStatementDto model.AccountStatementDto
	if len(accountRequestDto.Id) > 0 {
		var account, _ = getAccount(db, accountRequestDto.Id)
		if len(account.Id) > 0 {
			for i := range account.Statement {
				var payment, _ = getPayment(db, account.Statement[i])
				accountStatementDto.Statement = append(accountStatementDto.Statement, payment)
			}
		}
	}

	// Reply with Account Operation
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(accountStatementDto)

}
