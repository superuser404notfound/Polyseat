# Test fixtures

These are real `appmanifest_*.acf` files taken from a working Steam
installation, not handwritten ones.

That distinction is not decoration. An earlier parser in this project passed a
handwritten test and then failed on real files, because the handwriting left
out the very thing the parser tripped over. A fixture that was typed out by
whoever wrote the parser tends to contain exactly what the parser expects and
nothing else.

One change was made to each file: `LastOwner` carried the SteamID64 of a real
account, and this repository is public. It has been replaced with the SteamID
of Gabe Newell's public profile, which is about as non-private as a SteamID
gets. Everything else, including the whitespace, the field order and the
`InstalledDepots` block, is verbatim.

`libraryfolders-two.vdf` and `libraryfolders-three.vdf` are the exception, and
they say so here rather than pretending otherwise. Entry 0 is Steam's own, taken
from the same installation. The extra entries are added: the shared library as
the second folder, which is what older versions of Polyseat wrote there, and in
the three folder case one more standing in for a disk somebody added
themselves. They exist because that state is the one being migrated away from,
and no installation here is in it any more to copy from.
