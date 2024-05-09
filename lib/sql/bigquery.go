package sql

import (
	"fmt"
	"strings"
	"time"

	"github.com/artie-labs/transfer/lib/config/constants"
	"github.com/artie-labs/transfer/lib/typing"
	"github.com/artie-labs/transfer/lib/typing/ext"
)

const BQStreamingTimeFormat = "15:04:05"

const bqLayout = "2006-01-02 15:04:05 MST"

func BQExpiresDate(time time.Time) string {
	// BigQuery expects the timestamp to look in this format: 2023-01-01 00:00:00 UTC
	// This is used as part of table options.
	return time.Format(bqLayout)
}

type BigQueryDialect struct{}

func (BigQueryDialect) QuoteIdentifier(identifier string) string {
	// BigQuery needs backticks to quote.
	return fmt.Sprintf("`%s`", identifier)
}

func (BigQueryDialect) EscapeStruct(value string) string {
	return "JSON" + QuoteLiteral(value)
}

func (BigQueryDialect) DataTypeForKind(kindDetails typing.KindDetails, _ bool) string {
	// Doesn't look like we need to do any special type mapping.
	switch kindDetails.Kind {
	case typing.Float.Kind:
		return "float64"
	case typing.Array.Kind:
		// This is because BigQuery requires typing within the element of an array
		// IMO, a string type is the least controversial data type (others being bool, number, struct).
		// With String, we can always type cast the child elements.
		// BQ does this because 2d+ arrays are not allowed. See: https://cloud.google.com/bigquery/docs/reference/standard-sql/data-types#array_type
		return "array<string>"
	case typing.Struct.Kind:
		// Struct is a tighter version of JSON that requires type casting like Struct<int64>
		return "json"
	case typing.ETime.Kind:
		switch kindDetails.ExtendedTimeDetails.Type {
		case ext.DateTimeKindType:
			// https://cloud.google.com/bigquery/docs/reference/standard-sql/data-types#datetime_type
			// We should be using TIMESTAMP since it's an absolute point in time.
			return "timestamp"
		case ext.DateKindType:
			return "date"
		case ext.TimeKindType:
			return "time"
		}
	case typing.EDecimal.Kind:
		return kindDetails.ExtendedDecimalDetails.BigQueryKind()
	}

	return kindDetails.Kind
}

func (BigQueryDialect) KindForDataType(rawBqType string, _ string) (typing.KindDetails, error) {
	rawBqType = strings.ToLower(rawBqType)

	bqType := rawBqType
	if len(bqType) == 0 {
		return typing.Invalid, nil
	}

	idxStop := len(bqType)
	// Trim STRING (10) to String
	if idx := strings.Index(bqType, "("); idx > 0 {
		idxStop = idx
	}

	bqType = bqType[:idxStop]

	// Trim Struct<k type> to Struct
	idxStop = len(bqType)
	if idx := strings.Index(bqType, "<"); idx > 0 {
		idxStop = idx
	}

	// Geography, geometry date, time, varbinary, binary are currently not supported.
	switch strings.TrimSpace(bqType[:idxStop]) {
	case "numeric":
		if rawBqType == "numeric" || rawBqType == "bignumeric" {
			// This is a specific thing to BigQuery
			// A `NUMERIC` type without precision or scale specified is NUMERIC(38, 9)
			return typing.EDecimal, nil
		}

		return typing.ParseNumeric(typing.DefaultPrefix, rawBqType), nil
	case "bignumeric":
		if rawBqType == "bignumeric" {
			return typing.EDecimal, nil
		}

		return typing.ParseNumeric("bignumeric", rawBqType), nil
	case "decimal", "float", "float64", "bigdecimal":
		return typing.Float, nil
	case "int", "integer", "int64":
		return typing.Integer, nil
	case "varchar", "string":
		return typing.String, nil
	case "bool", "boolean":
		return typing.Boolean, nil
	case "struct", "json", "record":
		// Record is a legacy BQ object that maps to a JSON.
		return typing.Struct, nil
	case "array":
		return typing.Array, nil
	case "datetime", "timestamp":
		return typing.NewKindDetailsFromTemplate(typing.ETime, ext.DateTimeKindType), nil
	case "time":
		return typing.NewKindDetailsFromTemplate(typing.ETime, ext.TimeKindType), nil
	case "date":
		return typing.NewKindDetailsFromTemplate(typing.ETime, ext.DateKindType), nil
	default:
		return typing.Invalid, nil
	}
}

func (BigQueryDialect) IsColumnAlreadyExistsErr(err error) bool {
	// Error ends up looking like something like this: Column already exists: _string at [1:39]
	return strings.Contains(err.Error(), "Column already exists")
}

func (BigQueryDialect) BuildCreateTempTableQuery(fqTableName string, colSQLParts []string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (%s) OPTIONS (expiration_timestamp = TIMESTAMP("%s"))`,
		fqTableName, strings.Join(colSQLParts, ","), BQExpiresDate(time.Now().UTC().Add(constants.TemporaryTableTTL)))
}

func (BigQueryDialect) BuildProcessToastStructColExpression(colName string) string {
	return fmt.Sprintf(`CASE WHEN COALESCE(TO_JSON_STRING(cc.%s) != '{"key":"%s"}', true) THEN cc.%s ELSE c.%s END`,
		colName, constants.ToastUnavailableValuePlaceholder,
		colName, colName)
}