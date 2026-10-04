export type Chunk = { code: boolean; lang: string; text: string };

const OPEN = /^([ \t]*)(`{3,}|~{3,})([\s\S]*)$/;

// Splits Markdown fenced code blocks line by line in linear time. Fences may be
// indented (e.g. nested in list items); a fence closes only on its own line with
// at least as many of the same marker as opened it.
export function splitFences(text: string): Chunk[] {
  const lines = text.split("\n"),
    chunks: Chunk[] = [];
  let prose: string[] = [];
  const flush = () => {
    if (prose.join("").trim())
      chunks.push({ code: false, lang: "", text: prose.join("\n") });
    prose = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const open = lines[i].match(OPEN);
    // Backtick fences cannot have backticks in their info string (CommonMark).
    if (open && !(open[2][0] === "`" && open[3].includes("`"))) {
      const [, indent, marker, info] = open,
        close = new RegExp(`^[ \\t]*${marker[0]}{${marker.length},}\\s*$`),
        dedent = new RegExp(`^[ \\t]{0,${indent.length}}`);
      let end = i + 1;
      while (end < lines.length && !close.test(lines[end])) end++;
      if (end < lines.length) {
        flush();
        chunks.push({
          code: true,
          lang: info.trim(),
          text: lines
            .slice(i + 1, end)
            .map((line) => line.replace(dedent, ""))
            .join("\n"),
        });
        i = end;
        continue;
      }
      // Unclosed fence: nothing later can close it, so the rest is prose.
      prose = prose.concat(lines.slice(i));
      break;
    }
    prose.push(lines[i]);
  }
  flush();
  return chunks;
}
