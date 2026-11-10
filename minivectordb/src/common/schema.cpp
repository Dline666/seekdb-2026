#include "common/schema.h"

#include <cctype>
#include <string>

namespace mvdb {

bool names_equal(std::string_view lhs, std::string_view rhs) {
  if (lhs.size() != rhs.size()) return false;
  for (std::size_t i = 0; i < lhs.size(); ++i) {
    unsigned char const a = static_cast<unsigned char>(lhs[i]);
    unsigned char const b = static_cast<unsigned char>(rhs[i]);
    if (std::tolower(a) != std::tolower(b)) return false;
  }
  return true;
}

std::string lowercase(std::string_view text) {
  std::string out(text);
  for (char& c : out) {
    c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
  }
  return out;
}

int Schema::find_column(std::string_view column_name) const {
  for (std::size_t i = 0; i < columns.size(); ++i) {
    if (names_equal(columns[i].name, column_name)) return static_cast<int>(i);
  }
  return -1;
}

}  // namespace mvdb
