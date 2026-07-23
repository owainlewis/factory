use unicode_width::UnicodeWidthStr;

pub(crate) fn render<const COLUMNS: usize>(
    headings: [&str; COLUMNS],
    rows: &[[String; COLUMNS]],
    right_aligned: &[usize],
) -> String {
    let widths = std::array::from_fn(|column| {
        rows.iter()
            .map(|row| display_width(&row[column]))
            .chain([display_width(headings[column])])
            .max()
            .unwrap_or_default()
    });
    let mut output = String::new();

    write_row(
        &mut output,
        headings.each_ref().map(|heading| *heading),
        &widths,
        right_aligned,
    );
    write_row(
        &mut output,
        std::array::from_fn(|column| "─".repeat(widths[column])),
        &widths,
        &[],
    );
    for row in rows {
        write_row(&mut output, row.each_ref(), &widths, right_aligned);
    }

    output
}

fn write_row<T: AsRef<str>, const COLUMNS: usize>(
    output: &mut String,
    cells: [T; COLUMNS],
    widths: &[usize; COLUMNS],
    right_aligned: &[usize],
) {
    for (column, cell) in cells.iter().enumerate() {
        if column > 0 {
            output.push_str("  ");
        }
        let cell = cell.as_ref();
        let padding = widths[column].saturating_sub(display_width(cell));
        if right_aligned.contains(&column) {
            output.extend(std::iter::repeat_n(' ', padding));
            output.push_str(cell);
        } else {
            output.push_str(cell);
            if column + 1 < COLUMNS {
                output.extend(std::iter::repeat_n(' ', padding));
            }
        }
    }
    output.push('\n');
}

fn display_width(value: &str) -> usize {
    value.width()
}

#[cfg(test)]
mod tests {
    use super::render;

    #[test]
    fn aligns_text_and_numeric_columns() {
        let rows = [
            ["queued".to_owned(), "2".to_owned()],
            ["succeeded".to_owned(), "12".to_owned()],
        ];

        assert_eq!(
            render(["STATE", "ID"], &rows, &[1]),
            "STATE      ID\n─────────  ──\nqueued      2\nsucceeded  12\n"
        );
    }

    #[test]
    fn aligns_wide_and_combining_unicode_by_terminal_width() {
        let rows = [
            ["界".to_owned(), "1".to_owned()],
            ["e\u{301}".to_owned(), "12".to_owned()],
        ];

        assert_eq!(
            render(["NAME", "ID"], &rows, &[1]),
            "NAME  ID\n────  ──\n界     1\né     12\n"
        );
    }
}
