package billingv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/elastic/beats/v7/metricbeat/mb"
	"github.com/elastic/beats/v7/x-pack/metricbeat/module/aws"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/mapstr"
)

const metricsetName = "billingv2"

func init() {
	mb.Registry.MustAddMetricSet(aws.ModuleName, metricsetName, New,
		mb.DefaultMetricSet(),
	)
}

func (m *MetricSet) buildSQLQuery() string {
	query := fmt.Sprintf(m.Config.SQLQuery, m.Config.Database, m.Config.Table)

	return query
}

type Column struct {
	Name   string `config:"name"`
	Unique bool   `config:"unique"`
}

type Config struct {
	Database             string           `config:"database"`
	Table                string           `config:"table"`
	QueryResultsS3Bucket string           `config:"query_results_s3_bucket"`
	SQLQuery             string           `config:"sql_query"`
	Columns              []Column         `config:"columns"`
	ReuseQuery           ReuseQueryConfig `config:"reuse_query"`
}

type ReuseQueryConfig struct {
	Enabled         bool  `config:"enabled"`
	MaxAgeInMinutes int32 `config:"max_age_in_minutes"`
}

type MetricSet struct {
	*aws.MetricSet
	logger *logp.Logger
	Config Config `config:"athena_config"`
}

func New(base mb.BaseMetricSet) (mb.MetricSet, error) {
	logger := logp.NewLogger(metricsetName)
	metricSet, err := aws.NewMetricSet(base)
	if err != nil {
		return nil, fmt.Errorf("error creating aws metricset: %w", err)
	}

	config := struct {
		Config Config `config:"athena_config"`
	}{}

	err = base.Module().UnpackConfig(&config)
	if err != nil {
		return nil, fmt.Errorf("error unpack raw module config using UnpackConfig: %w", err)
	}

	logger.Debugf("athena config = %s", config)
	logger.Warnf("athena config = %s", config)

	return &MetricSet{
		MetricSet: metricSet,
		logger:    logger,
		Config:    config.Config,
	}, nil
}

func (m *MetricSet) Fetch(report mb.ReporterV2) error {
	var config aws.Config
	err := m.Module().UnpackConfig(&config)
	if err != nil {
		return err
	}

	rows, err := m.runQueryAndGetResults()
	if err != nil {
		m.Logger().Errorf("failed to execute query, %w", err)
		return err
	}

	events := make([]mb.Event, 0, len(rows))

	for _, row := range rows {
		eventID, err := m.generateEventID(row)
		if err != nil {
			return err
		}

		event := createDynamicEvent(eventID, row)
		events = append(events, event)
	}

	for _, event := range events {
		ok := report.Event(event)
		if !ok {
			m.Logger().Debugf("failed to send event, %v", event)
			return nil
		}
	}

	return nil
}

func createDynamicEvent(eventID string, row dynamicRow) mb.Event {
	event := mb.Event{
		ID:              eventID,
		MetricSetFields: mapstr.M{},
	}

	for key, value := range row.fields {
		_, _ = event.MetricSetFields.Put(key, value)
	}

	return event
}

type dynamicRow struct {
	fields map[string]any
}

type ResourceTag struct {
	Key   string
	Value string
}

func (t *ResourceTag) String() string {
	return fmt.Sprintf("%s:%s", t.Key, t.Value)
}

func (m *MetricSet) processRows(rows []types.Row, columnInfo []types.ColumnInfo) ([]dynamicRow, error) {
	res := make([]dynamicRow, 0, len(rows))

	for _, r := range rows[1:] {
		if len(r.Data) != len(m.Config.Columns) || len(columnInfo) != len(m.Config.Columns) {
			return nil, fmt.Errorf("invalid row length, expected %d, got %d", len(m.Config.Columns), len(r.Data))
		}

		processedRow := dynamicRow{
			fields: make(map[string]any),
		}

		for i, d := range r.Data {
			columnName := *columnInfo[i].Name
			columnType := *columnInfo[i].Type

			// add support for date, timestamp and other data types
			switch columnType {
			case "decimal":
				parsedColumnValue, err := strconv.ParseFloat(*d.VarCharValue, 64)
				if err != nil {
					m.Logger().Errorf("failed to parse decimal column value, %w", err)
					return nil, err
				}

				processedRow.fields[columnName] = parsedColumnValue
			case "varchar":
				processedRow.fields[columnName] = *d.VarCharValue
			case "map":
				v := *d.VarCharValue

				if v == "{}" {
					// empty map in aws
					processedRow.fields[columnName] = map[string]string{}
					continue
				}

				m.Logger().Warnf("here %s", v)

				parsedMap, err := parseToMap(v)
				if err != nil {
					m.Logger().Errorf("failed to parse map column value, %w", err)
					return nil, err
				}

				processedRow.fields[columnName] = parsedMap
			}
		}

		res = append(res, processedRow)
	}

	return res, nil
}

func parseToMap(input string) (map[string]string, error) {
	trimmedInput := strings.Trim(input, "{}")

	result := make(map[string]string)

	pairs := strings.Split(trimmedInput, ", ")

	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid input format for pair: %s", pair)
		}
		key := parts[0]
		value := parts[1]
		result[key] = value
	}

	return result, nil
}

func GetQueryExecution(ctx context.Context, client *athena.Client, QueryID *string, attempts int) error {
	t := time.NewTicker(time.Second * 5)
	defer t.Stop()

	attemptsFunc := func(o *athena.Options) { o.RetryMaxAttempts = attempts }

WAIT:
	for {
		select {
		case <-t.C:
			out, err := client.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{
				QueryExecutionId: QueryID,
			}, attemptsFunc)
			if err != nil {
				return err
			}

			switch out.QueryExecution.Status.State {
			case types.QueryExecutionStateCancelled,
				types.QueryExecutionStateFailed,
				types.QueryExecutionStateSucceeded:
				break WAIT
			}

		case <-ctx.Done():
			break WAIT
		}
	}

	return nil
}

func (m *MetricSet) runQueryAndGetResults() ([]dynamicRow, error) {
	awsBeatsConfig := m.MetricSet.AwsConfig.Copy()

	client := athena.NewFromConfig(awsBeatsConfig)

	query := m.buildSQLQuery()

	input := &athena.StartQueryExecutionInput{
		QueryString: awssdk.String(query),
		ResultConfiguration: &types.ResultConfiguration{
			OutputLocation: awssdk.String(m.Config.QueryResultsS3Bucket),
		},
	}

	if m.Config.ReuseQuery.Enabled {
		input.ResultReuseConfiguration = &types.ResultReuseConfiguration{
			ResultReuseByAgeConfiguration: &types.ResultReuseByAgeConfiguration{
				Enabled:         m.Config.ReuseQuery.Enabled,
				MaxAgeInMinutes: awssdk.Int32(m.Config.ReuseQuery.MaxAgeInMinutes),
			},
		}
	}

	result, err := client.StartQueryExecution(context.Background(), input)
	if err != nil {
		m.Logger().Errorf("failed to start query execution, %w", err)
		return nil, err
	}

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancelFunc()

	err = GetQueryExecution(ctx, client, result.QueryExecutionId, 10)
	if err != nil {
		m.Logger().Errorf("failed to start query execution, %w", err)
		return nil, err
	}

	var (
		rows       []types.Row
		columnInfo []types.ColumnInfo
		nextToken  *string
	)

	for {
		input := &athena.GetQueryResultsInput{
			QueryExecutionId: result.QueryExecutionId,
			NextToken:        nextToken,
		}

		result, err := client.GetQueryResults(context.Background(), input)
		if err != nil {
			m.Logger().Errorf("failed to get query results, %w", err)
			return nil, err
		}

		if len(columnInfo) == 0 {
			columnInfo = result.ResultSet.ResultSetMetadata.ColumnInfo
		}

		rows = append(rows, result.ResultSet.Rows...)
		nextToken = result.NextToken

		if nextToken == nil || len(result.ResultSet.Rows) == 0 {
			break
		}
	}

	processedRows, err := m.processRows(rows, columnInfo)
	if err != nil {
		m.Logger().Errorf("failed to process query results, %w", err)
		return nil, err
	}

	return processedRows, nil
}

func (m *MetricSet) generateEventID(row dynamicRow) (string, error) {
	var uniqueColumns []Column

	for _, column := range m.Config.Columns {
		if column.Unique {
			uniqueColumns = append(uniqueColumns, column)
		}
	}

	var eventID string

	for _, column := range uniqueColumns {
		value := row.fields[column.Name]
		eventID += fmt.Sprintf("%s", value)
	}

	h := sha256.New()
	h.Write([]byte(eventID))
	prefix := hex.EncodeToString(h.Sum(nil))

	return prefix[:20], nil
}
