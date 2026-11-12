// The server supplies this policy with the settings view. No password is sent
// for scoring or stored by this helper; the server checks again on save.
export function assessPassword(value, policy = {}) {
  const {min_length = 12, max_length = 256, common_passwords = [], sequences = []} = policy;
  let issue = "";
  if (/[^\x20-\x7e]/.test(value)) issue = "characters";
  else if (value.length < min_length || value.length > max_length) issue = "length";
  else if (value.trim() !== value) issue = "whitespace";
  const folded = value.toLowerCase(), normalized = folded.replace(/[^a-z0-9]/g, "");
  const stem = normalized.replace(/^\d+|\d+$/g, "");
  const leet = stem.replace(/[013457]/g, c => ({0:"o",1:"i",3:"e",4:"a",5:"s",7:"t"})[c]);
  if (!issue && common_passwords.some(common => [normalized, stem, leet].includes(common))) issue = "common";
  const candidates = normalized.length >= 6 ? [folded, normalized] : [folded];
  for (const candidate of candidates) {
    if (issue || !candidate) continue;
    for (let size = 1; size <= candidate.length / 2; size++) {
      if (candidate.length % size === 0 && candidate.slice(0,size).repeat(candidate.length / size) === candidate) {issue = "repeat"; break;}
    }
    if (!issue && candidate.length >= 6 && sequences.some(sequence => sequence.includes(candidate) || [...sequence].reverse().join("").includes(candidate))) issue = "sequence";
  }
  const messages = {
    length:`请输入 ${min_length}–${max_length} 个字符。`,
    characters:"请使用英文字符、数字、空格或符号。",
    whitespace:"密码首尾不能包含空白。",
    common:"这是常见密码，请换一个不易猜到的密码。",
    repeat:"请避免重复字符或重复组合。",
    sequence:"请避免连续数字、字母或键盘顺序。"
  };
  if (issue) return {issue, level:"weak", message:messages[issue]};
  const varied = new Set(normalized).size >= 6;
  if (!varied) return {issue:"", level:"weak", message:"较弱：字符变化较少，建议换成更难猜的内容。"};
  if (value.length < 16) return {issue:"", level:"fair", message:"一般：增加长度会更稳妥。"};
  return {issue:"", level:"strong", message:"较强：避免在其他网站重复使用。"};
}
