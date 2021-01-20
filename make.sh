#! /bin/bash

RM=`which rm` || (echo "Programm rm not found" && exit 1)
GO=`which go` || (echo "Programm go not found" && exit 1)
MKDIR=`which mkdir` || (echo "Programm mkdir not found" && exit 1)

`${RM} -rf ./binary`
`${MKDIR} ./binary`
for dir in ./robot_*/
do
    dir=${dir%*/}
    `${GO} build -o /usr/local/sbin/${dir##*/} ./${dir##*/}/*.go`
done