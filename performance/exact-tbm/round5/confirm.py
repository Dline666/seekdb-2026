"""Predeclared follow-ups for the observed tail regression and short timings."""
from run import compare

# Confirm 128D behavior with more query time per trial, preserving short runs.
compare('final', '10k-long', 10000, 128, 15000)
# Isolate the two PCALIGN directives while holding the scratch Go code fixed.
compare('alignment-on-scratch', 'tail127', 10000, 127, 3000, bins='scratch,final')
# Isolate scratch while holding the aligned assembly fixed.
compare('scratch-on-align', '10k', 10000, 128, 3000, bins='align,final')
compare('scratch-on-align', '100k', 100000, 256, 600, bins='align,final')
