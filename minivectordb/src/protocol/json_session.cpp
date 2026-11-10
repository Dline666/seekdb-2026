#include "protocol/json_session.h"

#include <cstdio>
#include <exception>
#include <string>
#include <string_view>

#ifdef _WIN32
#include <io.h>
#define MVDB_READ ::_read
#else
#include <unistd.h>
#define MVDB_READ ::read
#endif

#include "common/error.h"
#include "common/value.h"
#include "platform/fs_sync.h"
#include "protocol/json.h"

namespace mvdb {
namespace {

constexpr std::size_t kReadBlock = 64 * 1024;

std::string error_json(const std::string& id, const std::string& code,
                       const std::string& message) {
  return "{\"id\":" + id + ",\"ok\":false,\"error\":{\"code\":" +
         json::quote(code) + ",\"message\":" + json::quote(message) + "}}";
}

std::string value_to_json(const Value& value) {
  if (const auto* integer = std::get_if<std::int64_t>(&value)) {
    return std::to_string(*integer);
  }
  if (const auto* real = std::get_if<double>(&value)) {
    return json::number_to_json(*real);
  }
  if (const auto* text = std::get_if<std::string>(&value)) {
    return json::quote(*text);
  }
  const auto& vector = std::get<Vector>(value);
  std::string out = "[";
  for (std::size_t i = 0; i < vector.data.size(); ++i) {
    if (i != 0) out.push_back(',');
    out += json::float_to_json(vector.data[i]);
  }
  out.push_back(']');
  return out;
}

}  // namespace

JsonSession::JsonSession(Engine& engine, std::FILE* in, std::FILE* out)
    : engine_(engine), in_(in), out_(out) {}

bool JsonSession::read_line(std::string* line) {
  for (;;) {
    std::size_t const newline = buffer_.find('\n', pos_);
    if (newline != std::string::npos) {
      *line = buffer_.substr(pos_, newline - pos_);
      pos_ = newline + 1;
      if (pos_ >= buffer_.size()) {
        buffer_.clear();
        pos_ = 0;
      }
      if (!line->empty() && line->back() == '\r') line->pop_back();
      return true;
    }
    if (eof_) {
      if (pos_ < buffer_.size()) {
        *line = buffer_.substr(pos_);
        buffer_.clear();
        pos_ = 0;
        if (!line->empty() && line->back() == '\r') line->pop_back();
        return true;
      }
      return false;
    }
    if (pos_ > 0) {
      buffer_.erase(0, pos_);
      pos_ = 0;
    }
    std::size_t const old_size = buffer_.size();
    buffer_.resize(old_size + kReadBlock);
    // 必须用文件描述符的 read：fread 会等待读满请求长度，交互式驱动下会卡死。
    auto const count = MVDB_READ(file_descriptor(in_),
                                 buffer_.data() + old_size, kReadBlock);
    if (count <= 0) {
      buffer_.resize(old_size);
      eof_ = true;
      continue;
    }
    buffer_.resize(old_size + static_cast<std::size_t>(count));
  }
}

std::string JsonSession::render(const std::string& id,
                                const StatementResult& result) {
  if (result.status == Status::kError) {
    std::string const code =
        result.error_code.empty() ? "error" : result.error_code;
    return error_json(id, code, result.message);
  }
  if (result.status == Status::kNotImplemented) {
    return error_json(id, "not_implemented", "statement is not implemented");
  }

  std::string out = "{\"id\":" + id + ",\"ok\":true";
  if (!result.columns.empty()) {
    out += ",\"columns\":[";
    for (std::size_t i = 0; i < result.columns.size(); ++i) {
      if (i != 0) out.push_back(',');
      out += json::quote(result.columns[i]);
    }
    out += "],\"rows\":[";
    for (std::size_t r = 0; r < result.rows.size(); ++r) {
      if (r != 0) out.push_back(',');
      out.push_back('[');
      const std::vector<Value>& row = result.rows[r];
      for (std::size_t c = 0; c < row.size(); ++c) {
        if (c != 0) out.push_back(',');
        out += value_to_json(row[c]);
      }
      out.push_back(']');
    }
    out.push_back(']');
  } else if (result.has_affected_rows) {
    out += ",\"affected_rows\":" + std::to_string(result.affected_rows);
  }
  out.push_back('}');
  return out;
}

std::string JsonSession::handle(const json::Value& request, bool* stop) {
  std::string id = "null";
  if (request.type == json::Value::Type::kObject) {
    if (const json::Value* id_value = request.find("id");
        id_value != nullptr && !id_value->raw.empty()) {
      id = id_value->raw;
    }
  } else {
    return error_json(id, "invalid_request", "request must be an object");
  }

  const json::Value* op = request.find("op");
  std::string op_name;
  if (op != nullptr && op->type == json::Value::Type::kString) {
    op_name = op->string;
  }

  if (op_name == "shutdown") {
    engine_.shutdown();
    *stop = true;
    return "{\"id\":" + id + ",\"ok\":true}";
  }
  if (op_name != "execute") {
    return error_json(id, "invalid_request", "unknown op");
  }

  const json::Value* sql = request.find("sql");
  if (sql == nullptr || sql->type != json::Value::Type::kString) {
    return error_json(id, "invalid_request", "missing sql");
  }
  StatementResult const result = engine_.execute(sql->string);
  return render(id, result);
}

int JsonSession::run() {
  std::string line;
  while (read_line(&line)) {
    if (line.empty()) continue;
    std::string response;
    bool stop = false;
    try {
      json::Value const request = json::parse(line);
      response = handle(request, &stop);
    } catch (const SqlError& error) {
      response = error_json("null", "invalid_request", error.what());
    } catch (const std::exception& error) {
      response = error_json("null", "internal_error", error.what());
    }
    std::fwrite(response.data(), 1, response.size(), out_);
    std::fputc('\n', out_);
    std::fflush(out_);
    if (stop) break;
  }
  return 0;
}

}  // namespace mvdb
