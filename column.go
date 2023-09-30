//
// Copyright (c) 2017 Jon Carlson.  All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
//

package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/joncrlsn/misc"
	"github.com/joncrlsn/pgutil"
)

var (
	columnSqlTemplate = initColumnSqlTemplate()
)

// original
// {{if eq $.DbSchema "*" }}table_schema || '.' || {{end}}table_name || '.' || column_name AS compare_name

// the fix
// {{if eq $.DbSchema "*" }}table_schema || '.' || {{end}}table_name || '.' ||lpad(cast (ordinal_position as varchar), 5, '0')|| column_name AS compare_name

// Initializes the Sql template
func initColumnSqlTemplate() *template.Template {

	sql := `
SELECT table_schema
    ,  {{if eq $.DbSchema "*" }}table_schema || '.' || {{end}}table_name || '.' || column_name AS compare_name
	, table_name
    , column_name
    , data_type
    , is_nullable
    , column_default
    , character_maximum_length
    , is_identity
    , identity_generation
    , substring(udt_name from 2) AS array_type
FROM information_schema.columns
WHERE is_updatable = 'YES'
{{if eq $.DbSchema "*" }}
AND table_schema NOT LIKE 'pg_%' 
AND table_schema <> 'information_schema' 
{{else}}
AND table_schema = '{{$.DbSchema}}'
{{end}}
ORDER BY compare_name ASC;
`
	t := template.New("ColumnSqlTmpl")
	template.Must(t.Parse(sql))
	return t
}

var (
	tableColumnSqlTemplate = initTableColumnSqlTemplate()
)

// Initializes the Sql template
func initTableColumnSqlTemplate() *template.Template {
	sql := `
SELECT a.table_schema
    , {{if eq $.DbSchema "*" }}a.table_schema || '.' || {{end}}a.table_name || '.' || column_name  AS compare_name
	, a.table_name
    , column_name
    , data_type
    , is_nullable
    , column_default
    , character_maximum_length
FROM information_schema.columns a
INNER JOIN information_schema.tables b
    ON a.table_schema = b.table_schema AND
       a.table_name = b.table_name AND
       b.table_type = 'BASE TABLE'
WHERE is_updatable = 'YES'
{{if eq $.DbSchema "*" }}
AND a.table_schema NOT LIKE 'pg_%' 
AND a.table_schema <> 'information_schema' 
{{else}}
AND a.table_schema = '{{$.DbSchema}}'
{{end}}
{{ if $.TableType }}
AND b.table_type = '{{ $.TableType }}'
{{ end }}
ORDER BY compare_name ASC;
`
	t := template.New("ColumnSqlTmpl")
	template.Must(t.Parse(sql))
	return t
}

// ==================================
// Column Rows definition
// ==================================

// ColumnRows is a sortable slice of string maps
type ColumnRows []map[string]string

func (slice ColumnRows) Len() int {
	return len(slice)
}

func (slice ColumnRows) Less(i, j int) bool {
	return slice[i]["compare_name"] < slice[j]["compare_name"]
}

func (slice ColumnRows) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}

// ==================================
// ColumnSchema definition
// (implements Schema -- defined in pgdiff.go)
// ==================================

// ColumnSchema holds a slice of rows from one of the databases as well as
// a reference to the current row of data we're viewing.
type ColumnSchema struct {
	rows   ColumnRows
	rowNum int
	done   bool
}

// get returns the value from the current row for the given key
func (c *ColumnSchema) get(key string) string {
	if c.rowNum >= len(c.rows) {
		return ""
	}
	return c.rows[c.rowNum][key]
}

// NextRow increments the rowNum and tells you whether or not there are more
func (c *ColumnSchema) NextRow() bool {
	if c.rowNum >= len(c.rows)-1 {
		c.done = true
	}
	c.rowNum = c.rowNum + 1
	return !c.done
}

// Compare tells you, in one pass, whether or not the first row matches, is less than, or greater than the second row
func (c *ColumnSchema) Compare(obj interface{}) int {
	c2, ok := obj.(*ColumnSchema)
	if !ok {
		fmt.Println("Error!!!, Compare needs a ColumnSchema instance", c2)
	}

	val := misc.CompareStrings(c.get("compare_name"), c2.get("compare_name"))
	return val
}

// Add prints SQL to add the column
func (c *ColumnSchema) Add() {

	schema := dbInfo2.DbSchema
	if schema == "*" {
		schema = c.get("table_schema")
	}

	// Knowing the version of db2 would eliminate the need for this warning
	if c.get("is_identity") == "YES" {
		fmt.Println("-- WARNING: identity columns are not supported in PostgreSQL versions < 10.")
		fmt.Println("-- Attempting to create identity columns in earlier versions will probably result in errors.")
	}

	if c.get("data_type") == "character varying" {
		maxLength, valid := getMaxLength(c.get("character_maximum_length"))
		if !valid {
			fmt.Printf("ALTER TABLE %s.%s ADD COLUMN %s character varying", schema, c.get("table_name"), c.get("column_name"))
		} else {
			fmt.Printf("ALTER TABLE %s.%s ADD COLUMN %s character varying(%s)", schema, c.get("table_name"), c.get("column_name"), maxLength)
		}
	} else {
		dataType := c.get("data_type")
		//if c.get("data_type") == "ARRAY" {
		//fmt.Println("-- Note that adding of array data types are not yet generated properly.")
		//}
		if dataType == "ARRAY" {
			dataType = c.get("array_type") + "[]"
		}
		//fmt.Printf("ALTER TABLE %s.%s ADD COLUMN %s %s", schema, c.get("table_name"), c.get("column_name"), c.get("data_type"))
		fmt.Printf("ALTER TABLE %s.%s ADD COLUMN %s %s", schema, c.get("table_name"), c.get("column_name"), dataType)
	}

	if c.get("is_nullable") == "NO" {
		fmt.Printf(" NOT NULL")
	}
	if c.get("column_default") != "null" {
		fmt.Printf(" DEFAULT %s", c.get("column_default"))
	}
	// NOTE: there are more identity column sequence options according to the PostgreSQL
	// CREATE TABLE docs, but these do not appear to be available as of version 10.1
	if c.get("is_identity") == "YES" {
		fmt.Printf(" GENERATED %s AS IDENTITY", c.get("identity_generation"))
	}
	fmt.Printf(";\n")
}

// Drop prints SQL to drop the column
func (c *ColumnSchema) Drop() {
	// if dropping column
	fmt.Printf("ALTER TABLE %s.%s DROP COLUMN IF EXISTS %s;\n", c.get("table_schema"), c.get("table_name"), c.get("column_name"))
}

// Change handles the case where the table and column match, but the details do not
func (c *ColumnSchema) Change(obj interface{}) {
	c2, ok := obj.(*ColumnSchema)
	if !ok {
		fmt.Println("Error!!!, ColumnSchema.Change(obj) needs a ColumnSchema instance", c2)
	}

	// Adjust data type for array columns
	dataType1 := c.get("data_type")
	if dataType1 == "ARRAY" {
		dataType1 = c.get("array_type") + "[]"
	}
	dataType2 := c2.get("data_type")
	if dataType2 == "ARRAY" {
		dataType2 = c2.get("array_type") + "[]"
	}

	// Detect column type change (mostly varchar length, or number size increase)
	// (integer to/from bigint is OK)
	if dataType1 == dataType2 {
		if dataType1 == "character varying" {
			max1, max1Valid := getMaxLength(c.get("character_maximum_length"))
			max2, max2Valid := getMaxLength(c2.get("character_maximum_length"))
			if !max1Valid && !max2Valid {
				// Leave them alone, they both have undefined max lengths
			} else if (max1Valid || !max2Valid) && (max1 != c2.get("character_maximum_length")) {
				//if !max1Valid {
				//    fmt.Println("-- WARNING: varchar column has no maximum length.  Setting to 1024, which may result in data loss.")
				//}
				max1Int, err1 := strconv.Atoi(max1)
				check("converting string to int", err1)
				max2Int, err2 := strconv.Atoi(max2)
				check("converting string to int", err2)
				if max1Int < max2Int {
					fmt.Println("-- WARNING: The next statement will shorten a character varying column, which may result in data loss.")
				}
				fmt.Printf("-- max1Valid: %v  max2Valid: %v \n", max1Valid, max2Valid)
				fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s TYPE character varying(%s);\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"), max1)
			}
		}
	}

	// Code and test a column change from integer to bigint
	if dataType1 != dataType2 {
		fmt.Printf("-- WARNING: This type change may not work well: (%s to %s).\n", dataType2, dataType1)
		if strings.HasPrefix(dataType1, "character") {
			max1, max1Valid := getMaxLength(c.get("character_maximum_length"))
			if !max1Valid {
				fmt.Println("-- WARNING: varchar column has no maximum length.  Setting to 1024")
			}
			fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s TYPE %s(%s);\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"), dataType1, max1)
		} else {
			fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s TYPE %s;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"), dataType1)
		}
	}

	// Detect column default change (or added, dropped)
	if c.get("column_default") == "null" {
		if c2.get("column_default") != "null" {
			fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s DROP DEFAULT;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"))
		}
	} else if c.get("column_default") != c2.get("column_default") {
		fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s SET DEFAULT %s;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"), c.get("column_default"))
	}

	// Detect identity column change
	// Save result to variable instead of printing because order for adding/removing
	// is_nullable affects identity columns
	var identitySql string
	if c.get("is_identity") != c2.get("is_identity") {
		// Knowing the version of db2 would eliminate the need for this warning
		fmt.Println("-- WARNING: identity columns are not supported in PostgreSQL versions < 10.")
		fmt.Println("-- Attempting to create identity columns in earlier versions will probably result in errors.")
		if c.get("is_identity") == "YES" {
			identitySql = fmt.Sprintf("ALTER TABLE \"%s\".\"%s\" ALTER COLUMN \"%s\" ADD GENERATED %s AS IDENTITY;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"), c.get("identity_generation"))
		} else {
			identitySql = fmt.Sprintf("ALTER TABLE \"%s\".\"%s\" ALTER COLUMN \"%s\" DROP IDENTITY;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"))
		}
	}

	// Detect not-null and nullable change
	if c.get("is_nullable") != c2.get("is_nullable") {
		if c.get("is_nullable") == "YES" {
			if identitySql != "" {
				fmt.Printf(identitySql)
			}
			fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s DROP NOT NULL;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"))
		} else {
			fmt.Printf("ALTER TABLE %s.%s ALTER COLUMN %s SET NOT NULL;\n", c2.get("table_schema"), c.get("table_name"), c.get("column_name"))
			if identitySql != "" {
				fmt.Printf(identitySql)
			}
		}
	} else {
		if identitySql != "" {
			fmt.Printf(identitySql)
		}
	}
}

// ==================================
// Standalone Functions
// ==================================

// compare outputs SQL to make the columns match between two databases or schemas
func compare(conn1 *sql.DB, conn2 *sql.DB, tpl *template.Template) {
	buf1 := new(bytes.Buffer)
	tpl.Execute(buf1, dbInfo1)

	buf2 := new(bytes.Buffer)
	tpl.Execute(buf2, dbInfo2)

	rowChan1, _ := pgutil.QueryStrings(conn1, buf1.String())
	rowChan2, _ := pgutil.QueryStrings(conn2, buf2.String())

	//rows1 := make([]map[string]string, 500)
	rows1 := make(ColumnRows, 0)
	for row := range rowChan1 {
		rows1 = append(rows1, row)
	}
	sort.Sort(rows1)

	//rows2 := make([]map[string]string, 500)
	rows2 := make(ColumnRows, 0)
	for row := range rowChan2 {
		rows2 = append(rows2, row)
	}
	sort.Sort(&rows2)

	// We have to explicitly type this as Schema here for some unknown reason
	var schema1 Schema = &ColumnSchema{rows: rows1, rowNum: -1}
	var schema2 Schema = &ColumnSchema{rows: rows2, rowNum: -1}

	// Compare the columns
	doDiff(schema1, schema2)

}

// compareColumns outputs SQL to make the columns match between two databases or schemas
func compareColumns(conn1 *sql.DB, conn2 *sql.DB) {

	compare(conn1, conn2, columnSqlTemplate)

}

// compareColumns outputs SQL to make the tables columns (without views columns) match between two databases or schemas
func compareTableColumns(conn1 *sql.DB, conn2 *sql.DB) {

	compare(conn1, conn2, tableColumnSqlTemplate)

}

// getMaxLength returns the maximum length and whether or not it is valid
func getMaxLength(maxLength string) (string, bool) {

	if maxLength == "null" {
		// default to 1024
		return "1024", false
	}
	return maxLength, true
}
