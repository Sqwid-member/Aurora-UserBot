--[[
  Aurora "pulse" plugin — a minimal Lua example.

  Lua has no built-in JSON codec, so this file ships a ~40 line encoder and a
  ~40 line decoder. If your plugin needs more than that, use one of the many
  pure-Lua JSON libraries, or write the plugin in Python/Node where JSON is
  already in the standard library.
]]

local counters = { messages = 0, events = 0, started_at = os.time() }
local request_id = 0

--------------------------------------------------------------------------------
-- JSON encode
--------------------------------------------------------------------------------
local function json_encode(v)
  local t = type(v)
  if v == nil then return "null" end
  if t == "boolean" then return tostring(v) end
  if t == "number" then
    if v == math.floor(v) and math.abs(v) < 2 ^ 53 then return string.format("%d", v) end
    return tostring(v)
  end
  if t == "string" then
    local out = v:gsub('[%c"\\]', function(c)
      local map = { ['"'] = '\\"', ['\\'] = '\\\\', ['\n'] = '\\n', ['\r'] = '\\r', ['\t'] = '\\t' }
      return map[c] or string.format("\\u%04x", string.byte(c))
    end)
    return '"' .. out .. '"'
  end
  if t == "table" then
    if #v > 0 then
      local parts = {}
      for i = 1, #v do parts[i] = json_encode(v[i]) end
      return "[" .. table.concat(parts, ",") .. "]"
    end
    local parts, first = {}, true
    for k, val in pairs(v) do
      if not first then parts[#parts + 1] = "," end
      first = false
      parts[#parts + 1] = json_encode(tostring(k)) .. ":" .. json_encode(val)
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  return "null"
end

--------------------------------------------------------------------------------
-- JSON decode (objects and scalars are enough for the plugin protocol)
--------------------------------------------------------------------------------
local function json_decode(str)
  local pos = 1

  local function skip_ws()
    while pos <= #str and str:sub(pos, pos):match("%s") do pos = pos + 1 end
  end

  local parse_value

  local function parse_string()
    pos = pos + 1 -- opening quote
    local buf = {}
    while pos <= #str do
      local c = str:sub(pos, pos)
      if c == '"' then
        pos = pos + 1
        return table.concat(buf)
      elseif c == "\\" then
        local n = str:sub(pos + 1, pos + 1)
        local map = { n = "\n", t = "\t", r = "\r", ['"'] = '"', ["\\"] = "\\", ["/"] = "/" }
        if n == "u" then
          buf[#buf + 1] = string.char(tonumber(str:sub(pos + 2, pos + 5), 16) or 63)
          pos = pos + 6
        else
          buf[#buf + 1] = map[n] or n
          pos = pos + 2
        end
      else
        buf[#buf + 1] = c
        pos = pos + 1
      end
    end
    return table.concat(buf)
  end

  local function parse_number()
    local s, e = str:find("^-?%d+%.?%d*[eE]?[-+]?%d*", pos)
    local num = tonumber(str:sub(s, e))
    pos = e + 1
    return num
  end

  local function parse_table()
    pos = pos + 1
    local obj = {}
    skip_ws()
    if str:sub(pos, pos) == "}" then pos = pos + 1 return obj end
    while pos <= #str do
      skip_ws()
      local key
      if str:sub(pos, pos) == '"' then key = parse_string() else key = tostring(parse_value()) end
      skip_ws()
      if str:sub(pos, pos) == ":" then pos = pos + 1 end
      obj[key] = parse_value()
      skip_ws()
      local c = str:sub(pos, pos)
      pos = pos + 1
      if c == "}" then break end
    end
    return obj
  end

  function parse_value()
    skip_ws()
    local c = str:sub(pos, pos)
    if c == "{" then return parse_table() end
    if c == '"' then return parse_string() end
    if c == "t" then pos = pos + 4 return true end
    if c == "f" then pos = pos + 5 return false end
    if c == "n" then pos = pos + 4 return nil end
    return parse_number()
  end

  local ok, value = pcall(parse_value)
  if not ok then return nil end
  return value
end

--------------------------------------------------------------------------------
-- plugin plumbing
--------------------------------------------------------------------------------
local function log(level, message)
  io.stderr:write(string.format("[%s] %s\n", level:upper(), message))
end

local function write(frame)
  io.stdout:write(json_encode(frame) .. "\n")
  io.stdout:flush()
end

local function call(method, params)
  request_id = request_id + 1
  write({ jsonrpc = "2.0", id = request_id, method = method, params = params or {} })
end

local function reply(msg, result, err)
  local frame = { jsonrpc = "2.0", id = msg.id }
  if err then
    frame.error = { code = -32000, message = err }
  else
    frame.result = result or {}
  end
  write(frame)
end

local function format_duration(seconds)
  if seconds < 60 then return string.format("%d с", seconds) end
  if seconds < 3600 then return string.format("%d хв", seconds // 60) end
  return string.format("%d год %d хв", seconds // 3600, (seconds % 3600) // 60)
end

local function snapshot()
  local uptime = os.difftime(os.time(), counters.started_at)
  return string.format(
    "🛰 pulse працює %d с\n• повідомлень: %d\n• подій: %d",
    uptime, counters.messages, counters.events
  )
end

local function handle(msg)
  local method = msg.method

  if method == "plugin.hello" then
    log("info", "handshake with Aurora " .. tostring((msg.params or {}).version))
    reply(msg, { ok = true })
  elseif method == "plugin.load" then
    log("info", "loaded")
    call("ui.notify", { title = "Pulse", text = "моніторинг увімкнено", level = "info" })
    reply(msg, { ok = true })
  elseif method == "plugin.unload" then
    log("info", "unloading")
    reply(msg, { ok = true })
    os.exit(0)
  elseif method == "event" then
    local ev = msg.params or {}
    counters.events = counters.events + 1
    if ev.name == "message.new" then
      counters.messages = counters.messages + 1
    elseif ev.name == "session.started" then
      local user = ev.data or {}
      log("info", "session started: @" .. tostring(user.username or user.id))
    end
  elseif method == "command" then
    local params = msg.params or {}
    if params.name == "pulse" then
      reply(msg, snapshot())
    else
      reply(msg, nil, "unknown command " .. tostring(params.name))
    end
  end
end

for line in io.lines() do
  if #line > 0 then
    local ok, msg = pcall(json_decode, line)
    if ok and type(msg) == "table" then
      local success, err = pcall(handle, msg)
      if not success then log("error", tostring(err)) end
    end
  end
end
