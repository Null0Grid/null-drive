export interface Strength {
  score: 0 | 1 | 2 | 3 | 4;
  label: string;
  hint: string;
}

const common = [
  'password', 'passw0rd', 'qwerty', 'letmein', 'welcome', 'admin', 'iloveyou',
  'monkey', 'dragon', 'football', 'baseball', 'abc123', '123456', '111111',
  'nulldrive', 'changeme', 'secret',
];

/**
 * A deliberately simple strength estimate for the setup wizard: length
 * matters most, variety helps, obvious patterns are punished. It is a hint
 * for the user, not a security control (the server enforces the minimum
 * length; argon2id does the heavy lifting).
 */
export function passwordStrength(pw: string, username = ''): Strength {
  if (pw.length === 0) return { score: 0, label: '', hint: 'At least 8 characters. A few random words work well.' };
  if (pw.length < 8) return { score: 0, label: 'Too short', hint: `${8 - pw.length} more character${pw.length === 7 ? '' : 's'} needed.` };

  const lower = pw.toLowerCase();
  if (common.some((c) => lower.includes(c)) || (username && lower.includes(username.toLowerCase()))) {
    return { score: 1, label: 'Weak', hint: 'Avoid common words and your username.' };
  }
  if (/^(.)\1+$/.test(pw) || /^(0123456789|1234567890|abcdefghij)/.test(lower)) {
    return { score: 1, label: 'Weak', hint: 'Avoid repeats and sequences.' };
  }

  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length;
  let points = 0;
  if (pw.length >= 10) points++;
  if (pw.length >= 14) points++;
  if (pw.length >= 20) points++;
  if (classes >= 3) points++;
  if (new Set(pw).size < pw.length / 3) points--; // lots of repetition

  if (points <= 0) return { score: 1, label: 'Weak', hint: 'Make it longer, e.g. add another word.' };
  if (points === 1) return { score: 2, label: 'Fair', hint: 'Longer is better: aim for 14+ characters.' };
  if (points === 2) return { score: 3, label: 'Good', hint: 'Solid. A password manager can remember it for you.' };
  return { score: 4, label: 'Strong', hint: 'Excellent.' };
}
