# Illume: scriptable command line program for LLM interfacing

A unix filter for talking to an OpenAI-compatible LLM API. Sends standard
input to the LLM and streams its response to standard output. In a text
editor like Vim, send your buffer into the program via standard input and
append its output to your buffer.

## How to build

With Go 1.7 or later:

    $ go build illume.go

Then place `illume` on your `$PATH`.

## How to use

A couple of examples running outside of a text editor:

    $ illume <request.md >response.md
    $ illume <chat.md | tee -a chat.md

`illume.vim` has a Vim configuration for interacting with live output:

* `Illume()`: complete the end the buffer (chat, `!completion`)
* `IllumeInfill()`: generate code at the cursor
* `IllumeStop()`: stop generation in this buffer

`illume.el` is similar for Emacs: `M-x illume` and `M-x illume-stop`.

## Example usage

Use `!context` to select files to upload as context. These are uploaded in
full, mind the token limit and narrow the context as needed by pointing to
subdirectories or temporarily deleting files. Put `!user` on its own line,
then your question:

    !context /path/to/repository .py .sql

    !user

    Do you suggest any additional indexes?

Sending this to `illume` retrieves a reply:

    !context /path/to/repository .py .sql

    !user

    Do you suggest any additional indexes?

    !assistant

    Yes, your XYZ table...

Add your response with another `!user`:

    !context /path/to/repository .py .sql

    !user

    Do you suggest any additional indexes?

    !assistant

    Yes, your XYZ table...

    !user

    But what about ...?

Rinse and repeat. The text file is the entire state of the conversation.

### Completion mode

Alternatively the LLM can continue from text of your input using the
`!complete` directive.

    !completion
    The meaning of life is

Do not use `!user` nor `!assistant` in this mode, but the other options
still work.

### Infill mode

If the input contains `!infill` by itself, Illume operates in infill mode.
Output is to be inserted in place of `!infill`, i.e. code generation. By
default it will use the llama.cpp `/infill` endpoint, which requires a
FIM-trained model with metadata declaring its FIM tokens. This excludes
most models, including most "coder" models due to missing metadata. There
are currently no standards and few conventions around FIM, and every model
implements it differently.

Given an argument, it is memorized as the template, replacing `{prefix}`
and `{suffix}` with the surrounding input. For example, including a
leading space in the template:

    !infill  <PRE> {prefix} <SUF>{suffix} <MID>

Write this template according to the model's FIM documentation. Illume
includes built-in `fim:MODEL` templates for several popular models. This
form of `!infill` only configures, and does not activate infill mode on
its own. Put it in a profile.

For example, if generate FIM completions on a remote Codestral running on
llama.cpp, your Illume profile file might be something like:

    !profile llama.cpp
    !profile fim:mistral
    !api http://myllama:8080/

With `illume.vim`, do not type a no-argument `!infill` directive yourself.
The configuration automatically inserts it into Illume's input at the
cursor position.

**Recommendation**: DeepSeek produces the best FIM output, followed by
Qwen and Granite. Both also work out-of-the-box with llama.cpp `/infill`.

## Environment

`$ILLUME_PROFILE` selects an API profile, which configures a URL and
flavor. The program has several built-in profiles (see `Profiles` in the
source). If this variable contains a slash, the contents of the file at
that path are prepended to all inputs. Use this to set the URL, extra
keys, HTTP headers, or even a system prompt.

## Directives

An `!error` "directive" appears in error output, but it's not processed on
input. Everything before `!user` and `!assistant` are in the "system"
role, which is where you can write a system prompt.

### `!profile NAME`

Load a profile. JSON `!:KEY` directives in the profile do not override
user-set keys. If no `!profile` is given, Illume loads `$ILLUME_PROFILE`
if set, otherwise it loads the default profile.

### `!api URL`

Sets the API base URL. When not llama.cpp, it typically ends with `/v1` or
`/v2`. Illume interpolates `{…}` in the URL from `!:KEY` directives. It's
done just before making the request, and so may reference keys set after
the `!api` directive. Examples:

    !api https://api-inference.huggingface.co/models/{model}/v1
    !:model mistralai/Mistral-Nemo-Instruct-2407

If the URL is wrapped in quotes, it will be used literally as provided
without modification.

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

    !:temperature 0.3
    !:model mistralai/Mistral-Nemo-Instruct-2407
    !:stop ["<|im_end|>"]

If `VALUE` is missing, the key is deleted instead. If it cannot be parsed
as JSON, it's passed through as a string. If it looks like JSON but should
be sent as string data, wrap it in quotes to turn it into a JSON string.

### `!>HEADER VALUE`

Insert an arbitrary HTTP header into the request. Examples:

    !>x-use-cache false
    !>user-agent My LLM Client 1.0
    !>authorization

If `VALUE` is missing, the header is deleted. This is, for instance, a
second for disabling the API token, as shown in the example. If the value
contains `$VAR` then Illume will expand it from the environment.

### `!completion`

Use completion mode instead of conversational. The LLM will continue
writing from the end of the document. Cannot be used with `!user` or
`!assistant`, which are for the (default) chat mode.

### `!infill [TEMPLATE]`

With no template, activate infill mode, and generate code to be inserted
at this position. Given a template, use that template to generate the
prompt when infill mode is active.

### `!reddit FILE`

Like `!context` but embed a reddit post from its JSON representation
(append `.json` to the URL and then download it). Includes all comments
with threading.

    !reddit some-reddit-post.json
    Please summarize this reddit post and its comments.

### `!reddit! FILE`

Like `!reddit` but just the post with no comments.

### `!stats`

On response completion, inserts a `!note` with timing statistics.

### `!debug`

Dry run: "reply" with the raw HTTP request instead of querying the API.
For inspecting the exact query parameters.
