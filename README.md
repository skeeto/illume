# Illume: scriptable command line program for LLM interfacing

A unix filter for talking to an OpenAI-compatible LLM API. Sends standard
input to the LLM and streams its response to standard output. In a text
editor like Vim, send your buffer into the program via standard input and
append its output to your buffer.

## How to build

    $ go build

Then place `illume` on your `$PATH`.

## How to use

A couple of examples running outside of a text editor:

    $ illume <request.md >response.md
    $ illume <chat.md | tee -a chat.md

A Vim configuration that animates the streaming output:

```vim
function! IllumeAppend(channel, msg)
    let buf = ch_getbufnr(a:channel, 'err')
    let msg = split(a:msg, '\n', 1)
    for i in range(len(msg))
        call setbufline(buf, '$', getbufline(buf, '$')[0] .. msg[i])
        if i < len(msg) - 1
            call appendbufline(buf, '$', '')
        endif
    endfor
endfunction

function! IllumeStop()
    call job_stop(b:assistant_job)
endfunction

function! Illume()
    let b:assistant_job = job_start(['illume'], #{
        \ in_io: 'buffer',
        \ in_buf: bufnr(),
        \ out_cb: function('IllumeAppend'),
        \ out_mode: 'raw',
        \ err_io: 'buffer',
        \ err_buf: bufnr(),
        \ })
endfunction

map <leader>a :call Illume()<cr>
map <leader>s :call IllumeStop()<cr>
map <leader>u Go<cr>!user<cr><cr>
```

## Example usage

Use `!context` to select files to upload as context. These are uploaded in
full, mind the token limit and narrow the context as needed by pointing to
subdirectories or temporarily deleting files. Put `!user` on its own line,
then your question:

```
!context /path/to/repository .py .sql

!user

Do you suggest any additional indexes?
```

Sending this to `illume` retrieves a reply:

```
!context /path/to/repository .py .sql

!user

Do you suggest any additional indexes?

!assistant

Yes, your XYZ table...
```

Add your response with another `!user`:

```
!context /path/to/repository .py .sql

!user

Do you suggest any additional indexes?

!assistant

Yes, your XYZ table...

!user

But what about ...?
```

Rinse and repeat. The text file is the entire state of the conversation.

### Completion mode

Alternatively the LLM can continue from text of your input using the
`!complete` directive.

```
!completion
The meaning of life is
```

Do not use `!user` nor `!assistant` in this mode, but the other options
still work.

## Environment

`$ILLUME_TOKEN` provides the API token. If unset no API token is sent.

`$ILLUME_PROFILE` selects an API profile, which configures a URL and
flavor. The program has several built-in profiles (see `Profiles` in the
source). If this variable contains a slash, the contents of the file at
that path are prepended to all inputs. Use this to set the URL, extra
keys, HTTP headers, or even a system prompt.

## Directives

An `!error` "directive" appears in error output, but it's not processed on
input. Everything before `!user` and `!assistant` are in the "system"
sole, which is where you can write a system prompt.

### `!profile NAME`

Load a profile. JSON `!:KEY` directives in the profile do not override
user-set keys. If no `!profile` is given, Illume loads `$ILLUME_PROFILE`
if set, otherwise it loads the default profile.

### `!token [TOKEN]`

Sets an API token. Overrides `$ILLUME_TOKEN`. Most useful for setting a
token in a custom profile. Given no token, none will be sent.

### `!api URL`

Sets the API base URL. Typically ends with `v1` or `v2`. Any `{…}` in the
URL interpolates JSON values from `!:KEY` directives. This is done just
before making the request, and so may reference keys set after the `!api`
directive. Examples:

    !api http://localhost:8080/v1
    !api https://api-inference.huggingface.co/models/{model}/v1

### `!context FILE`

Insert a file at this position in the conversation.

### `!context DIR [SUFFIX...]`

Include all files under DIR with matching file name suffixes. Only
relative names are sent, but the last element of `DIR` is included in this
relative path if it does not end with a slash. Files can be included in
any role, not just the system prompt.

### `!user`

Marks the following lines as belonging to a user message. You can modify
these to trick the LLM into thinking you said something different in the
past.

### `!assistant`

Marks the following lines as belonging to an assistant message. You can
modify these to trick the LLM into thinking it said something different.

### `!note ...`

These lines are not sent to the LLM. Used to annotate conversations.

### `!begin`

Discard all messages before this line. Used to "comment out" headers in
the input, e.g. when composing email. Directives before this line are
still effective.

### `!end`

Stop processing directives and ignore the rest of the input.

### `!:KEY VALUE`

Insert an arbitrary JSON value into the query object. Examples:

```
!:temperature 0.3
!:model mistralai/Mistral-Nemo-Instruct-2407
!:stop ["<|im_end|>"]
```

If `VALUE` is missing, the key is deleted instead. If it cannot be parsed
as JSON, it's passed through as a string. If it looks like JSON but should
be sent as string data, wrap it in quotes to turn it into a JSON string.

### `!>HEADER VALUE`

Insert an arbitrary HTTP header into the request. Examples:

```
!>x-use-cache false
!>user-agent My LLM Client 1.0
!>authorization
```

If `VALUE` is missing, the header is deleted. This is, for instance, a
second for disabling the API token, as shown in the example.

### `!completion`

Use completion mode instead of conversational. The LLM will continue
writing from the end of the document. Cannot be used with `!user` or
`!assistant`, which are for the (default) chat mode.

### `!debug`

Dry run: "reply" with the raw HTTP request instead of querying the API.
For inspecting the exact query parameters.
