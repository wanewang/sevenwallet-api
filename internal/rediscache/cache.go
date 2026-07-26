// Package rediscache stores LI.FI, Moralis, and CoinGecko cache records in Redis.
package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"wallet-api/internal/cmcmarket"
	"wallet-api/internal/lifi"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/tokenvalidity"
)

// Cache is a Redis-backed store for LI.FI, Moralis, and CoinGecko records.
type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// New parses a redis URL (redis://host:port/db) and builds a Cache.
func New(redisURL string, ttl time.Duration) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Cache{client: redis.NewClient(opt), ttl: ttl}, nil
}

// Ping verifies connectivity.
func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close releases the client.
func (c *Cache) Close() error { return c.client.Close() }

func key(chain string) string { return "lifi:tokens:" + chain }

type payload struct {
	FetchedAt time.Time        `json:"fetchedAt"`
	Tokens    []lifi.ListToken `json:"tokens"`
}

type marketPayload struct {
	marketdata.Record
	FetchedAtUnixNano string `json:"fetchedAtUnixNano"`
}

type cmcMarketPayload struct {
	cmcmarket.Record
	FetchedAtUnixNano string `json:"fetchedAtUnixNano"`
}

// marketCASTemplate is the shared timestamp-CAS script. The single
// --[[PAYLOAD_VALIDATION]] marker is where each provider's payload validation
// goes, supplied by marketCASScript. Deriving one script from another script's
// source text let a whitespace edit silently disarm the validation.
const marketCASTemplate = `
local function decimalParts(value)
  if type(value) ~= 'string' then
    return nil
  end
  local start = 1
  local negative = false
  if string.sub(value, 1, 1) == '-' then
    negative = true
    start = 2
  end
  if start > string.len(value) then
    return nil
  end
  for i = start, string.len(value) do
    local digit = string.byte(value, i)
    if digit < 48 or digit > 57 then
      return nil
    end
  end
  local digits = string.sub(value, start)
  digits = string.gsub(digits, '^0+', '')
  if digits == '' then
    return false, '0'
  end
  return negative, digits
end

local function compareDecimals(left, right)
  local leftNegative, leftDigits = decimalParts(left)
  local rightNegative, rightDigits = decimalParts(right)
  if leftDigits == nil or rightDigits == nil then
    return nil
  end
  if leftNegative ~= rightNegative then
    if leftNegative then
      return -1
    end
    return 1
  end
  if string.len(leftDigits) ~= string.len(rightDigits) then
    if leftNegative then
      if string.len(leftDigits) < string.len(rightDigits) then
        return 1
      end
      return -1
    end
    if string.len(leftDigits) < string.len(rightDigits) then
      return -1
    end
    return 1
  end
  if leftDigits == rightDigits then
    return 0
  end
  if leftNegative then
    if leftDigits < rightDigits then
      return 1
    end
    return -1
  end
  if leftDigits < rightDigits then
    return -1
  end
  return 1
end

local function compareUnsigned(left, right)
  if string.len(left) ~= string.len(right) then
    if string.len(left) < string.len(right) then
      return -1
    end
    return 1
  end
  if left == right then
    return 0
  end
  if left < right then
    return -1
  end
  return 1
end

local function canonicalInt64(value)
  if type(value) ~= 'string' or value == '' then
    return false
  end
  local start = 1
  local negative = false
  if string.sub(value, 1, 1) == '-' then
    negative = true
    start = 2
  end
  if start > string.len(value) then
    return false
  end
  if string.sub(value, start, start) == '0' and start < string.len(value) then
    return false
  end
  for i = start, string.len(value) do
    local digit = string.byte(value, i)
    if digit < 48 or digit > 57 then
      return false
    end
  end
  local digits = string.sub(value, start)
  if negative and digits == '0' then
    return false
  end
  if string.len(digits) > 19 then
    return false
  end
  if string.len(digits) == 19 then
    local limit = '9223372036854775807'
    if negative then
      limit = '9223372036854775808'
    end
    if compareUnsigned(digits, limit) == 1 then
      return false
    end
  end
  return true
end

local function stripLeadingZeros(value)
  local digits = string.gsub(value, '^0+', '')
  if digits == '' then
    return '0'
  end
  return digits
end

local function integerString(value)
  if value == 0 then
    return '0'
  end
  local result = ''
  while value > 0 do
    local digit = value % 10
    result = string.char(48 + digit) .. result
    value = math.floor(value / 10)
  end
  return result
end

local function addUnsigned(left, right)
  local i = string.len(left)
  local j = string.len(right)
  local carry = 0
  local result = ''
  while i >= 1 or j >= 1 or carry > 0 do
    local total = carry
    if i >= 1 then
      total = total + string.byte(left, i) - 48
      i = i - 1
    end
    if j >= 1 then
      total = total + string.byte(right, j) - 48
      j = j - 1
    end
    result = string.char(48 + total % 10) .. result
    carry = math.floor(total / 10)
  end
  return stripLeadingZeros(result)
end

local function subtractUnsigned(left, right)
  local i = string.len(left)
  local j = string.len(right)
  local borrow = 0
  local result = ''
  while i >= 1 do
    local difference = string.byte(left, i) - 48 - borrow
    if j >= 1 then
      difference = difference - string.byte(right, j) + 48
      j = j - 1
    end
    if difference < 0 then
      difference = difference + 10
      borrow = 1
    else
      borrow = 0
    end
    result = string.char(48 + difference) .. result
    i = i - 1
  end
  return stripLeadingZeros(result)
end

local function multiplyUnsignedByInteger(value, multiplier)
  local i = string.len(value)
  local carry = 0
  local result = ''
  while i >= 1 do
    local total = (string.byte(value, i) - 48) * multiplier + carry
    result = string.char(48 + total % 10) .. result
    carry = math.floor(total / 10)
    i = i - 1
  end
  while carry > 0 do
    result = string.char(48 + carry % 10) .. result
    carry = math.floor(carry / 10)
  end
  return stripLeadingZeros(result)
end

local function parseDigits(value, start, count)
  if start + count - 1 > string.len(value) then
    return nil
  end
  local result = 0
  for i = start, start + count - 1 do
    local digit = string.byte(value, i)
    if digit < 48 or digit > 57 then
      return nil
    end
    result = result * 10 + digit - 48
  end
  return result
end

local function leapYear(year)
  return year % 4 == 0 and (year % 100 ~= 0 or year % 400 == 0)
end

local function timestampUnixNano(value)
  if type(value) ~= 'string' or string.len(value) < 20 then
    return nil
  end
  local year = parseDigits(value, 1, 4)
  local month = parseDigits(value, 6, 2)
  local day = parseDigits(value, 9, 2)
  local hour = parseDigits(value, 12, 2)
  local minute = parseDigits(value, 15, 2)
  local second = parseDigits(value, 18, 2)
  if not year or not month or not day or not hour or not minute or not second or
      string.sub(value, 5, 5) ~= '-' or string.sub(value, 8, 8) ~= '-' or
      string.sub(value, 11, 11) ~= 'T' or string.sub(value, 14, 14) ~= ':' or
      string.sub(value, 17, 17) ~= ':' then
    return nil
  end
  if month < 1 or month > 12 or hour > 23 or minute > 59 or second > 59 then
    return nil
  end
  local monthDays = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
  if month == 2 and leapYear(year) then
    monthDays[2] = 29
  end
  if day < 1 or day > monthDays[month] then
    return nil
  end

  local position = 20
  local nanos = 0
  local fractionMarker = string.sub(value, 20, 20)
  if fractionMarker == '.' or fractionMarker == ',' then
    position = position + 1
    local fractionStart = position
    while position <= string.len(value) do
      local digit = string.byte(value, position)
      if digit < 48 or digit > 57 then
        break
      end
      position = position + 1
    end
    local fractionDigits = position - fractionStart
    if fractionDigits < 1 or fractionDigits > 9 then
      return nil
    end
    nanos = parseDigits(value, fractionStart, fractionDigits)
    for _ = fractionDigits + 1, 9 do
      nanos = nanos * 10
    end
  end

  local offsetSeconds = 0
  local zone = string.sub(value, position, position)
  if zone == 'Z' then
    if position ~= string.len(value) then
      return nil
    end
  elseif zone == '+' or zone == '-' then
    if position + 5 ~= string.len(value) or string.sub(value, position + 3, position + 3) ~= ':' then
      return nil
    end
    local offsetHour = parseDigits(value, position + 1, 2)
    local offsetMinute = parseDigits(value, position + 4, 2)
    if not offsetHour or not offsetMinute or offsetHour > 23 or offsetMinute > 59 then
      return nil
    end
    offsetSeconds = offsetHour * 3600 + offsetMinute * 60
    if zone == '-' then
      offsetSeconds = -offsetSeconds
    end
  else
    return nil
  end

  local adjustedYear = year
  if month <= 2 then
    adjustedYear = adjustedYear - 1
  end
  local era = math.floor(adjustedYear / 400)
  local yearOfEra = adjustedYear - era * 400
  local monthOfYear
  if month > 2 then
    monthOfYear = month - 3
  else
    monthOfYear = month + 9
  end
  local dayOfYear = math.floor((153 * monthOfYear + 2) / 5) + day - 1
  local dayOfEra = yearOfEra * 365 + math.floor(yearOfEra / 4) - math.floor(yearOfEra / 100) + dayOfYear
  local days = era * 146097 + dayOfEra - 719468
  local seconds = days * 86400 + hour * 3600 + minute * 60 + second - offsetSeconds
  local negative = seconds < 0
  local absoluteSeconds = math.abs(seconds)
  local magnitude = multiplyUnsignedByInteger(integerString(absoluteSeconds), 1000000000)
  if negative then
    if nanos > 0 then
      magnitude = subtractUnsigned(magnitude, integerString(nanos))
    end
    if magnitude == '0' then
      return '0'
    end
    return '-' .. magnitude
  end
  magnitude = addUnsigned(magnitude, integerString(nanos))
  return magnitude
end

local function validEnvelope(decoded, expectedChain, expectedToken)
  if type(decoded) ~= 'table' then
    return false
  end
  if decoded['chain'] ~= expectedChain or decoded['tokenKey'] ~= expectedToken then
    return false
  end
--[[PAYLOAD_VALIDATION]]
  if not canonicalInt64(decoded['fetchedAtUnixNano']) then
    return false
  end
  local expectedVersion = timestampUnixNano(decoded['fetchedAt'])
  return expectedVersion ~= nil and expectedVersion == decoded['fetchedAtUnixNano']
end

local current = redis.call('GET', KEYS[1])
if current then
  local ok, decoded = pcall(cjson.decode, current)
  if ok and validEnvelope(decoded, ARGV[4], ARGV[5]) then
    if compareDecimals(ARGV[2], decoded['fetchedAtUnixNano']) == -1 then
      return 0
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return 1
`

const casValidationMarker = "--[[PAYLOAD_VALIDATION]]"

// marketCASScript builds one provider's CAS script from the shared template.
// A missing marker panics at init rather than silently yielding a script with
// no payload validation at all.
func marketCASScript(validation string) string {
	if !strings.Contains(marketCASTemplate, casValidationMarker) {
		panic("rediscache: market CAS template lost its " + casValidationMarker + " marker")
	}
	return strings.Replace(marketCASTemplate, casValidationMarker, validation, 1)
}

var marketCASLua = marketCASScript(
	`  local required = {'chain', 'tokenKey', 'coingeckoID', 'fetchedAt', 'fetchedAtUnixNano'}
  for _, field in ipairs(required) do
    if type(decoded[field]) ~= 'string' or decoded[field] == '' then
      return false
    end
  end`)

var cmcMarketCASLua = marketCASScript(
	`  local required = {'chain', 'tokenKey', 'fetchedAt', 'fetchedAtUnixNano'}
  if type(decoded['coinMarketCapID']) ~= 'number' or decoded['coinMarketCapID'] <= 0 then
    return false
  end
  for _, field in ipairs(required) do
    if type(decoded[field]) ~= 'string' or decoded[field] == '' then
      return false
    end
  end`)

// SaveTokenList writes the list with the configured safety TTL.
func (c *Cache) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	b, err := json.Marshal(payload{FetchedAt: fetchedAt, Tokens: tokens})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key(chain), b, c.ttl).Err()
}

// LoadTokenList returns the cached list, if present.
func (c *Cache) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	b, err := c.client.Get(ctx, key(chain)).Bytes()
	if err == redis.Nil {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, time.Time{}, false, err
	}
	return p.Tokens, p.FetchedAt, true, nil
}

func metaKey(chain, address string) string {
	return "tokenmeta:" + chain + ":" + strings.ToLower(address)
}

// SaveTokenMeta caches a verdict/metadata record with the given TTL.
func (c *Cache) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record, ttl time.Duration) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, metaKey(chain, address), b, ttl).Err()
}

// LoadTokenMeta returns the cached verdict/metadata record, if present.
func (c *Cache) LoadTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error) {
	b, err := c.client.Get(ctx, metaKey(chain, address)).Bytes()
	if err == redis.Nil {
		return tokenvalidity.Record{}, false, nil
	}
	if err != nil {
		return tokenvalidity.Record{}, false, err
	}
	var r tokenvalidity.Record
	if err := json.Unmarshal(b, &r); err != nil {
		return tokenvalidity.Record{}, false, err
	}
	return r, true, nil
}

func marketKey(k marketdata.Key) string {
	return "coingecko:market:" + k.Chain + ":" + k.TokenKey
}

// LoadMarketData loads requested CoinGecko records with one Redis MGET.
// Malformed individual entries are logged and treated as misses.
func (c *Cache) LoadMarketData(ctx context.Context, keys []marketdata.Key) (map[marketdata.Key]marketdata.Record, error) {
	result := make(map[marketdata.Key]marketdata.Record, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = marketKey(key)
	}
	values, err := c.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		var raw []byte
		switch v := value.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			log.Printf("rediscache: malformed CoinGecko market data key %q: unexpected Redis value %T", redisKeys[i], value)
			continue
		}

		record, ok := decodeMarketPayload(raw, keys[i])
		if !ok {
			log.Printf("rediscache: malformed CoinGecko market data key %q: invalid envelope", redisKeys[i])
			continue
		}
		result[keys[i]] = record
	}
	return result, nil
}

// SaveMarketData writes positive-TTL CoinGecko records in one pipeline. Each
// Lua operation atomically rejects a fetch older than the stored record.
func (c *Cache) SaveMarketData(ctx context.Context, writes []marketdata.CacheWrite) error {
	pipe := c.client.Pipeline()
	queued := 0
	for _, write := range writes {
		if write.TTL <= 0 {
			continue
		}
		payload, err := json.Marshal(marketPayload{
			Record:            write.Record,
			FetchedAtUnixNano: strconv.FormatInt(write.Record.FetchedAt.UnixNano(), 10),
		})
		if err != nil {
			return err
		}
		ttlMilliseconds := write.TTL.Milliseconds()
		if ttlMilliseconds < 1 {
			ttlMilliseconds = 1
		}
		pipe.Eval(ctx, marketCASLua, []string{marketKey(write.Record.Key)},
			string(payload), strconv.FormatInt(write.Record.FetchedAt.UnixNano(), 10), strconv.FormatInt(ttlMilliseconds, 10),
			write.Record.Key.Chain, write.Record.Key.TokenKey)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

func decodeMarketPayload(raw []byte, key marketdata.Key) (marketdata.Record, bool) {
	var payload marketPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return marketdata.Record{}, false
	}
	if payload.Record.Chain != key.Chain || payload.Record.TokenKey != key.TokenKey ||
		payload.Record.CoinGeckoID == "" || payload.Record.FetchedAt.IsZero() {
		return marketdata.Record{}, false
	}
	fetchedAtUnixNano, err := strconv.ParseInt(payload.FetchedAtUnixNano, 10, 64)
	if err != nil || strconv.FormatInt(fetchedAtUnixNano, 10) != payload.FetchedAtUnixNano ||
		fetchedAtUnixNano != payload.Record.FetchedAt.UnixNano() {
		return marketdata.Record{}, false
	}
	return payload.Record, true
}

func cmcMarketKey(k marketdata.Key) string {
	return "cmc:market:" + k.Chain + ":" + k.TokenKey
}

// LoadCoinMarketCapMarketData loads requested CMC records with one Redis MGET.
func (c *Cache) LoadCoinMarketCapMarketData(ctx context.Context, keys []marketdata.Key) (map[marketdata.Key]cmcmarket.Record, error) {
	result := make(map[marketdata.Key]cmcmarket.Record, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = cmcMarketKey(key)
	}
	values, err := c.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		var raw []byte
		switch v := value.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			log.Printf("rediscache: malformed CMC market data key %q: unexpected Redis value %T", redisKeys[i], value)
			continue
		}
		record, ok := decodeCMCMarketPayload(raw, keys[i])
		if !ok {
			log.Printf("rediscache: malformed CMC market data key %q: invalid envelope", redisKeys[i])
			continue
		}
		result[keys[i]] = record
	}
	return result, nil
}

// SaveCoinMarketCapMarketData stores positive-TTL CMC records with timestamp CAS.
func (c *Cache) SaveCoinMarketCapMarketData(ctx context.Context, writes []cmcmarket.CacheWrite) error {
	pipe := c.client.Pipeline()
	queued := 0
	for _, write := range writes {
		if write.TTL <= 0 {
			continue
		}
		payload, err := json.Marshal(cmcMarketPayload{
			Record:            write.Record,
			FetchedAtUnixNano: strconv.FormatInt(write.Record.FetchedAt.UnixNano(), 10),
		})
		if err != nil {
			return err
		}
		ttlMilliseconds := write.TTL.Milliseconds()
		if ttlMilliseconds < 1 {
			ttlMilliseconds = 1
		}
		pipe.Eval(ctx, cmcMarketCASLua, []string{cmcMarketKey(write.Record.Key)},
			string(payload), strconv.FormatInt(write.Record.FetchedAt.UnixNano(), 10), strconv.FormatInt(ttlMilliseconds, 10),
			write.Record.Key.Chain, write.Record.Key.TokenKey)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

func decodeCMCMarketPayload(raw []byte, key marketdata.Key) (cmcmarket.Record, bool) {
	var payload cmcMarketPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return cmcmarket.Record{}, false
	}
	if payload.Record.Chain != key.Chain || payload.Record.TokenKey != key.TokenKey ||
		payload.Record.CoinMarketCapID <= 0 || payload.Record.FetchedAt.IsZero() {
		return cmcmarket.Record{}, false
	}
	fetchedAtUnixNano, err := strconv.ParseInt(payload.FetchedAtUnixNano, 10, 64)
	if err != nil || strconv.FormatInt(fetchedAtUnixNano, 10) != payload.FetchedAtUnixNano ||
		fetchedAtUnixNano != payload.Record.FetchedAt.UnixNano() {
		return cmcmarket.Record{}, false
	}
	return payload.Record, true
}
