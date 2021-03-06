package intf

/*
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/apache/incubator-trafficcontrol/lib/go-log"
	"github.com/apache/incubator-trafficcontrol/lib/go-tc"
	"github.com/apache/incubator-trafficcontrol/lib/go-tc/v13"
	"github.com/apache/incubator-trafficcontrol/traffic_ops/traffic_ops_golang/api"
	"github.com/apache/incubator-trafficcontrol/traffic_ops/traffic_ops_golang/auth"
	"github.com/apache/incubator-trafficcontrol/traffic_ops/traffic_ops_golang/dbhelpers"
	"github.com/apache/incubator-trafficcontrol/traffic_ops/traffic_ops_golang/tovalidate"

	validation "github.com/go-ozzo/ozzo-validation"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

//we need a type alias to define functions on
type TOInterface v13.InterfaceNullable

//the refType is passed into the handlers where a copy of its type is used to decode the json.
var refType = TOInterface(v13.InterfaceNullable{})

func GetRefType() *TOInterface {
	return &refType
}

func (intf TOInterface) GetKeyFieldsInfo() []api.KeyFieldInfo {
	return []api.KeyFieldInfo{{"id", api.GetIntKey}}
}

//Implementation of the Identifier, Validator interface functions
func (intf TOInterface) GetKeys() (map[string]interface{}, bool) {
	if intf.ID == nil {
		return map[string]interface{}{"id": 0}, false
	}
	return map[string]interface{}{"id": *intf.ID}, true
}

func (intf *TOInterface) SetKeys(keys map[string]interface{}) {
	i, _ := keys["id"].(int) //this utilizes the non panicking type assertion, if the thrown away ok variable is false i will be the zero of the type, 0 here.
	intf.ID = &i
}

//Implementation of the Identifier, Validator interface functions
func (intf *TOInterface) GetID() (int, bool) {
	if intf.ID == nil {
		return 0, false
	}
	return *intf.ID, true
}

func (intf *TOInterface) GetAuditName() string {
	if intf.InterfaceName != nil {
		return *intf.InterfaceName
	}
	id, _ := intf.GetID()
	return strconv.Itoa(id)
}

func (intf *TOInterface) GetType() string {
	return "intf"
}

func (intf *TOInterface) SetID(i int) {
	intf.ID = &i
}

func (intf *TOInterface) Validate(db *sqlx.DB) []error {
	validateErrs := validation.Errors{
		"serverId":      validation.Validate(intf.ServerID, validation.NotNil),
		"interfaceName": validation.Validate(intf.InterfaceName, validation.NotNil),
	}
	errs := tovalidate.ToErrors(validateErrs)
	if len(errs) > 0 {
		return errs
	}

	rows, err := db.Query("select id from server where id=$1", intf.ServerID)
	if err != nil {
		log.Error.Printf("could not execute select id from server: %s\n", err)
		errs = append(errs, tc.DBError)
		return errs
	}
	defer rows.Close()
	if !rows.Next() {
		errs = append(errs, errors.New("invalid server id"))
	}

	return errs
}

func (intf *TOInterface) Read(db *sqlx.DB, params map[string]string, user auth.CurrentUser) ([]interface{}, []error, tc.ApiErrorType) {
	var rows *sqlx.Rows

	// Query Parameters to Database Query column mappings
	// see the fields mapped in the SQL query
	queryParamsToQueryCols := map[string]dbhelpers.WhereColumnInfo{
		"serverId": dbhelpers.WhereColumnInfo{"if.server", nil},
		"id":       dbhelpers.WhereColumnInfo{"if.id", api.IsInt},
	}
	where, orderBy, queryValues, errs := dbhelpers.BuildWhereAndOrderBy(params, queryParamsToQueryCols)
	if len(errs) > 0 {
		return nil, errs, tc.DataConflictError
	}

	query := selectQuery() + where + orderBy
	log.Debugln("Query is ", query)

	rows, err := db.NamedQuery(query, queryValues)
	if err != nil {
		log.Errorf("Error querying Interface: %v", err)
		return nil, []error{tc.DBError}, tc.SystemError
	}
	defer rows.Close()

	interfaces := []interface{}{}
	for rows.Next() {
		var p v13.InterfaceNullable
		if err = rows.StructScan(&p); err != nil {
			log.Errorf("error parsing Interface rows: %v", err)
			return nil, []error{tc.DBError}, tc.SystemError
		}
		interfaces = append(interfaces, p)
	}

	return interfaces, []error{}, tc.NoError

}

func selectQuery() string {
	selectStmt := `SELECT
if.id,
if.server as server_id,
s.host_name as server,
if.interface_name,
if.interface_mtu,
if.last_updated

FROM interface if

JOIN server s ON if.server = s.id`

	return selectStmt
}

//The TOInterface implementation of the Updater interface
//all implementations of Updater should use transactions and return the proper errorType
//ParsePQUniqueConstraintError is used to determine if a cdn with conflicting values exists
//if so, it will return an errorType of DataConflict and the type should be appended to the
//generic error message returned
func (intf *TOInterface) Update(db *sqlx.DB, user auth.CurrentUser) (error, tc.ApiErrorType) {
	rollbackTransaction := true
	tx, err := db.Beginx()
	defer func() {
		if tx == nil || !rollbackTransaction {
			return
		}
		err := tx.Rollback()
		if err != nil {
			log.Errorln(errors.New("rolling back transaction: " + err.Error()))
		}
	}()

	if err != nil {
		log.Error.Printf("could not begin transaction: %v", err)
		return tc.DBError, tc.SystemError
	}

	err, errType := intf.UpdateExecAndCheck(tx)
	if err != nil {
		return err, errType
	}

	err = tx.Commit()
	if err != nil {
		log.Errorln("Could not commit transaction: ", err)
		return tc.DBError, tc.SystemError
	}
	rollbackTransaction = false
	return nil, tc.NoError
}

func (intf *TOInterface) UpdateExecAndCheck(tx *sqlx.Tx) (error, tc.ApiErrorType) {
	log.Debugf("about to run exec query: %s with interface: %++v", updateQuery(), intf)
	resultRows, err := tx.NamedQuery(updateQuery(), intf)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok {
			err, eType := dbhelpers.ParsePQUniqueConstraintError(pqErr)
			if eType == tc.DataConflictError {
				return errors.New("an interface with " + err.Error()), eType
			}
			return err, eType
		} else {
			log.Errorf("received error: %++v from update execution", err)
			return tc.DBError, tc.SystemError
		}
	}
	defer resultRows.Close()

	var lastUpdated tc.TimeNoMod
	rowsAffected := 0
	for resultRows.Next() {
		rowsAffected++
		if err := resultRows.Scan(&lastUpdated); err != nil {
			log.Error.Printf("could not scan lastUpdated from insert: %s\n", err)
			return tc.DBError, tc.SystemError
		}
	}
	log.Debugf("lastUpdated: %++v", lastUpdated)
	intf.LastUpdated = &lastUpdated
	if rowsAffected != 1 {
		if rowsAffected < 1 {
			return errors.New("no interface found with this id"), tc.DataMissingError
		} else {
			return fmt.Errorf("this update affected too many rows: %d", rowsAffected), tc.SystemError
		}
	}

	return nil, tc.NoError
}

func updateQuery() string {
	query := `UPDATE
interface SET
interface_name=:interface_name,
interface_mtu=:interface_mtu
WHERE id=:id RETURNING last_updated`
	return query
}

//The TOInterface implementation of the Inserter interface
//all implementations of Inserter should use transactions and return the proper errorType
//ParsePQUniqueConstraintError is used to determine if a interface with conflicting values exists
//if so, it will return an errorType of DataConflict and the type should be appended to the
//generic error message returned
//The insert sql returns the id and lastUpdated values of the newly inserted interface and have
//to be added to the struct
func (intf *TOInterface) Create(db *sqlx.DB, user auth.CurrentUser) (error, tc.ApiErrorType) {
	rollbackTransaction := true
	tx, err := db.Beginx()
	defer func() {
		if tx == nil || !rollbackTransaction {
			return
		}
		err := tx.Rollback()
		if err != nil {
			log.Errorln(errors.New("rolling back transaction: " + err.Error()))
		}
	}()

	if err != nil {
		log.Error.Printf("could not begin transaction: %v", err)
		return tc.DBError, tc.SystemError
	}

	err, errType := intf.InsertExecAndCheck(tx)
	if err != nil {
		return err, errType
	}

	err = tx.Commit()
	if err != nil {
		log.Errorln("Could not commit transaction: ", err)
		return tc.DBError, tc.SystemError
	}
	rollbackTransaction = false
	return nil, tc.NoError
}

func (intf *TOInterface) InsertExecAndCheck(tx *sqlx.Tx) (error, tc.ApiErrorType) {
	resultRows, err := tx.NamedQuery(insertQuery(), intf)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok {
			err, eType := dbhelpers.ParsePQUniqueConstraintError(pqErr)
			if eType == tc.DataConflictError {
				return errors.New("an interface with " + err.Error()), eType
			}
			return err, eType
		} else {
			log.Errorf("received non pq error: %++v from create execution", err)
			return tc.DBError, tc.SystemError
		}
	}
	defer resultRows.Close()

	var id int
	var lastUpdated tc.TimeNoMod
	rowsAffected := 0
	for resultRows.Next() {
		rowsAffected++
		if err := resultRows.Scan(&id, &lastUpdated); err != nil {
			log.Error.Printf("could not scan id from insert: %s\n", err)
			return tc.DBError, tc.SystemError
		}
	}
	if rowsAffected == 0 {
		err = errors.New("no interface was inserted, no id was returned")
		log.Errorln(err)
		return tc.DBError, tc.SystemError
	} else if rowsAffected > 1 {
		err = errors.New("too many ids returned from interface insert")
		log.Errorln(err)
		return tc.DBError, tc.SystemError
	}
	intf.SetID(id)
	intf.LastUpdated = &lastUpdated
	return nil, tc.NoError
}

func insertQuery() string {
	query := `INSERT INTO interface (
server,
interface_name,
interface_mtu) VALUES (
:server_id,
:interface_name,
:interface_mtu) RETURNING id,last_updated`
	return query
}

//The TOInterface implementation of the Deleter interface
//all implementations of Deleter should use transactions and return the proper errorType
func (intf *TOInterface) Delete(db *sqlx.DB, user auth.CurrentUser) (error, tc.ApiErrorType) {

	// delete interface with primary IP assigned is NOT allowed
	rows, err := db.Query("select t.name from interface intf join ip ip on intf.id=ip.interface join type t on ip.type=t.id where intf.id=$1", intf.ID)
	if err != nil {
		log.Error.Printf("could not execute select t.name from interface intf join ip ip on intf.id=ip.interface join type t on ip.type=t.id: %s\n", err)
		return tc.DBError, tc.SystemError
	}
	defer rows.Close()
	var typeName string
	for rows.Next() {
		if err := rows.Scan(&typeName); err != nil {
			log.Error.Printf("could not scan t.name from interface intf join ip ip on intf.id=ip.interface join type t on ip.type=t.id: %s\n", err)
			return tc.DBError, tc.SystemError
		}
	}
	if typeName == "IP_PRIMARY" {
		log.Error.Printf("delete interface with primary IP assigned is not allowed by this API\n")
		return errors.New("delete interface with primary IP assigned is not allowed by this API"), tc.ForbiddenError
	}

	rollbackTransaction := true
	tx, err := db.Beginx()
	defer func() {
		if tx == nil || !rollbackTransaction {
			return
		}
		err := tx.Rollback()
		if err != nil {
			log.Errorln(errors.New("rolling back transaction: " + err.Error()))
		}
	}()

	if err != nil {
		log.Error.Printf("could not begin transaction: %v", err)
		return tc.DBError, tc.SystemError
	}
	log.Debugf("about to run exec query: %s with interface: %++v", deleteQuery(), intf)
	result, err := tx.NamedExec(deleteQuery(), intf)
	if err != nil {
		log.Errorf("received error: %++v from delete execution", err)
		return tc.DBError, tc.SystemError
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return tc.DBError, tc.SystemError
	}
	if rowsAffected != 1 {
		if rowsAffected < 1 {
			return errors.New("no interface with that id found"), tc.DataMissingError
		} else {
			return fmt.Errorf("this create affected too many rows: %d", rowsAffected), tc.SystemError
		}
	}
	err = tx.Commit()
	if err != nil {
		log.Errorln("Could not commit transaction: ", err)
		return tc.DBError, tc.SystemError
	}
	rollbackTransaction = false
	return nil, tc.NoError
}

func deleteQuery() string {
	query := `DELETE FROM interface
WHERE id=:id`
	return query
}
