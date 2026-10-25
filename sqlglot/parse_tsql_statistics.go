package sqlglot

import "strings"

func (p *parser) parseTSQLUpdateStatistics() (*Expression, error) {
	p.advance() // UPDATE STATISTICS is one token
	if p.atStatementEnd() || p.at(TokEND) || p.at(TokWITH) {
		return nil, p.unsupported("UPDATE STATISTICS without a table")
	}
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	node := New("UpdateStatistics", Arg{"this", table})
	if p.at(TokL_PAREN) {
		indexes, err := p.parseWrappedCSV(p.parseIdentifier)
		if err != nil || len(indexes) == 0 {
			return nil, p.unsupported("UPDATE STATISTICS without index names")
		}
		node.Set("expressions", indexes)
	} else if !p.atStatementEnd() && !p.at(TokWITH) {
		index, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		node.Set("expressions", []*Expression{index})
	}
	if p.match(TokWITH) {
		var options []*Expression
		for {
			option, err := p.parseTSQLStatisticsOption()
			if err != nil {
				return nil, err
			}
			options = append(options, option)
			if !p.match(TokCOMMA) {
				break
			}
		}
		node.Set("options", options)
	}
	return node, nil
}

func (p *parser) parseTSQLStatisticsOption() (*Expression, error) {
	if p.atWordsAny("FULLSCAN", "RESAMPLE", "ALL", "COLUMNS", "INDEX", "NORECOMPUTE") {
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		return New("Var", Arg{"this", word}), nil
	}
	if p.matchUnquotedWord("SAMPLE") {
		if !p.at(TokNUMBER) {
			return nil, p.unsupported("UPDATE STATISTICS SAMPLE without a number")
		}
		value, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		if !p.atWordsAny("PERCENT", "ROWS") {
			return nil, p.unsupported("UPDATE STATISTICS SAMPLE without PERCENT or ROWS")
		}
		unit := strings.ToUpper(p.curr().Text)
		p.advance()
		return New("StatisticsSample", Arg{"this", value}, Arg{"unit", unit}), nil
	}
	if p.atWordsAny("MAXDOP", "ROWCOUNT", "PAGECOUNT", "STATS_STREAM", "INCREMENTAL", "PERSIST_SAMPLE_PERCENT", "AUTO_DROP") {
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		if !p.match(TokEQ) {
			return nil, p.unsupported("UPDATE STATISTICS option without =")
		}
		var value *Expression
		if word == "INCREMENTAL" || word == "PERSIST_SAMPLE_PERCENT" || word == "AUTO_DROP" {
			if !p.atWordsAny("ON", "OFF") {
				return nil, p.unsupported("UPDATE STATISTICS option without ON or OFF")
			}
			value = New("Var", Arg{"this", strings.ToUpper(p.curr().Text)})
			p.advance()
		} else {
			if !p.at(TokNUMBER) && (word != "STATS_STREAM" || !p.at(TokHEX_STRING)) {
				return nil, p.unsupported("UPDATE STATISTICS option without a numeric value")
			}
			var err error
			value, err = p.parsePrimary()
			if err != nil {
				return nil, err
			}
		}
		return New("EQ", Arg{"this", New("Var", Arg{"this", word})}, Arg{"expression", value}), nil
	}
	return nil, p.unsupported("an UPDATE STATISTICS option this port does not read")
}
