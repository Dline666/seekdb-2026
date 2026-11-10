#include "protocol/formatter.h"

namespace mvdb {
namespace {

void append_text_row(std::string* out, const std::vector<std::string>& row) {
  for (std::size_t i = 0; i < row.size(); ++i) {
    if (i != 0) out->push_back('\t');
    out->append(row[i]);
  }
  out->push_back('\n');
}

void append_value_row(std::string* out, const std::vector<Value>& row) {
  for (std::size_t i = 0; i < row.size(); ++i) {
    if (i != 0) out->push_back('\t');
    out->append(value_to_string(row[i]));
  }
  out->push_back('\n');
}

}  // namespace

std::string TabularFormatter::format(std::string_view /*sql*/,
                                     const StatementResult& result) const {
  std::string out;
  if (result.status == Status::kError) {
    out.append("ERR ").append(result.message).append("\n");
    return out;
  }
  if (result.status == Status::kNotImplemented) {
    out.append("NOT_IMPLEMENTED\n");
    return out;
  }
  if (result.columns.empty()) {
    out.append("OK ").append(std::to_string(result.affected_rows)).append("\n");
    return out;
  }
  out.append("ROWS ").append(std::to_string(result.rows.size())).append("\n");
  append_text_row(&out, result.columns);
  for (const std::vector<Value>& row : result.rows) {
    append_value_row(&out, row);
  }
  return out;
}

std::string EchoFormatter::format(std::string_view sql,
                                  const StatementResult& result) const {
  std::string out;
  out.append("> ").append(sql.data(), sql.size()).append("\n");
  switch (result.status) {
    case Status::kOk:
      if (!result.columns.empty()) {
        out.append("rows=").append(std::to_string(result.rows.size()))
            .append("\n");
      } else {
        out.append("OK ").append(std::to_string(result.affected_rows))
            .append("\n");
      }
      break;
    case Status::kError:
      out.append("ERR ").append(result.message).append("\n");
      break;
    case Status::kNotImplemented:
      out.append("NOT_IMPLEMENTED\n");
      break;
  }
  return out;
}

}  // namespace mvdb
