create extension if not exists fuzzystrmatch;

-- Validate trigram candidates against whole words (or contiguous phrases),
-- not isolated syllables spread across unrelated metadata. The indexed gram
-- predicate remains the candidate generator; this is the final spelling gate.
create function catalog_search_spelling_match(needle text, terms text)
returns boolean
language plpgsql
immutable strict parallel safe
cost 1000
as $$
declare
    -- Track suffixes and extensions are boundaries too: a misspelled book
    -- name must still match Name000001.m4b without treating its number as
    -- six extra spelling edits. Contiguous phrases can rejoin these tokens.
    words text[] := regexp_split_to_array(
        regexp_replace(regexp_replace(terms,
            '([[:alpha:]])([[:digit:]])', '\1 \2', 'g'),
            '([[:digit:]])([[:alpha:]])', '\1 \2', 'g'),
        '[^[:alnum:]]+');
    needle_length integer := char_length(needle);
    budget integer := case when needle_length >= 8 then 2 else 1 end;
    phrase text;
    phrase_length integer;
    first_word integer;
    last_word integer;
begin
    -- fuzzystrmatch accepts at most 255 characters. Longer queries still
    -- have the exact substring path and must never raise a search error.
    if needle_length < 3 or needle_length > 255 then
        return false;
    end if;
    for first_word in 1..coalesce(array_length(words, 1), 0) loop
        phrase := '';
        for last_word in first_word..array_length(words, 1) loop
            phrase := phrase || words[last_word];
            phrase_length := char_length(phrase);
            exit when phrase_length > least(255, needle_length + budget);
            if phrase_length >= needle_length - budget and
               levenshtein_less_equal(needle, phrase, budget) <= budget then
                return true;
            end if;
        end loop;
    end loop;
    return false;
end
$$;
