#include "protocol/session.h"

#include <cstdio>
#include <string>
#include <string_view>

#ifdef MVDB_TIMING
#include <chrono>
#endif

#include "common/error.h"
#include "protocol/statement_reader.h"
#include "sql/parser.h"

namespace mvdb {
namespace {

// 单行语句的完整性判定：解析成功即完整；解析在结尾处报错说明还没写完。
bool statement_complete(std::string_view sql) {
  try {
    sql::parse(sql);
    return true;
  } catch (const SqlError& error) {
    return std::string_view(error.what()).find("<end of statement>") ==
           std::string_view::npos;
  }
}

}  // namespace

Session::Session(Engine& engine, const Formatter& formatter, std::FILE* out)
    : engine_(engine), formatter_(formatter), out_(out) {}

int Session::run() {
  // 评测按语句往返计时，每条语句写完都必须刷新。
  std::setvbuf(out_, nullptr, _IOFBF, 64 * 1024);

  StatementReader reader(0, statement_complete);
  std::string sql;
#ifdef MVDB_TIMING
  double read_seconds = 0;
  double execute_seconds = 0;
  auto read_start = std::chrono::steady_clock::now();
#endif
  while (true) {
#ifdef MVDB_TIMING
    read_start = std::chrono::steady_clock::now();
#endif
    bool const has = reader.next(&sql);
#ifdef MVDB_TIMING
    auto const after_read = std::chrono::steady_clock::now();
    read_seconds +=
        std::chrono::duration<double>(after_read - read_start).count();
#endif
    if (!has) break;
    StatementResult const result = engine_.execute(sql);
#ifdef MVDB_TIMING
    auto const after_execute = std::chrono::steady_clock::now();
    execute_seconds +=
        std::chrono::duration<double>(after_execute - after_read).count();
#endif
    std::string const text = formatter_.format(sql, result);
    std::fwrite(text.data(), 1, text.size(), out_);
    std::fflush(out_);
  }
#ifdef MVDB_TIMING
  std::fprintf(stderr, "timing: reader=%.3f session_exec=%.3f\n", read_seconds,
               execute_seconds);
#endif
  std::fflush(out_);
  return 0;
}

}  // namespace mvdb
